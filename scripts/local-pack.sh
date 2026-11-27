#!/usr/bin/env bash
# local-pack.sh - bring the local provider pack up on this host, prove a claim, take it down.
#
# The local pack (infra/providers/local) starts the machines of a Sovereign as privileged
# containers on one host with Docker, and boots each control-plane machine with the shared
# template infra/providers/_shared/cloudinit-control-plane.tftpl. This script is how CI (the
# workflow .github/workflows/conformance-local.yaml) and later suites use it:
#
#   bash scripts/local-pack.sh image
#   bash scripts/local-pack.sh charts [--changed <file>]
#   bash scripts/local-pack.sh up --regions <1|2> --branch <name> --commit <sha> [--changed <file>]
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
# charts     Prints, from the source, which charts the pack installs and from where. Flux
#            applies the Kustomization bootstrap-kit, whose path the local module sets to the
#            kit KIT_PATH (written by scripts/generators/local-kit.py: the slot files of
#            clusters/_template/bootstrap-kit of the releases the pack runs and of their
#            dependencies, and the pack's own files). Each slot's HelmRelease takes its chart from
#            a HelmRepository at PUBLISHED_URL, at the version the slot pins: the chart that
#            blueprint-release.yaml published from main, not the chart of the change under test.
#            The release local-path takes the upstream chart from the GitRepository of
#            rancher/local-path-provisioner. With --changed <file> (one changed path per line,
#            relative to the tree) a release whose chart is published from a folder of this tree
#            (platform/<name>/chart, products/<name>/chart) is marked TREE when a changed path lies
#            in that folder or in the folder of one of its file:// dependencies (kit_plan). Its
#            last line says which catalyst-api image the console runs (api_plan): TREE when a
#            changed path is a path the image is built from (api_sources: the Containerfile
#            API_CONTAINERFILE and every source it copies from the build context, the root of the
#            tree, as catalyst-build.yaml builds it); otherwise the image that the chart of the
#            console release names (images.catalystApi.tag), the one main published.
# up         Writes the module inputs from the contract fixture of 1 or 2 regions (one
#            control-plane machine per region; one worker machine in the primary region, none
#            elsewhere) and the machine image of image above (built there when it is not yet
#            on this host), runs tofu init and apply, then on each control-plane machine: pins
#            the GitRepository flux-system/openova to <branch> at <commit> as soon as the
#            template has applied its Flux objects, and puts the pull credential of GHCR_USER
#            and GHCR_TOKEN into the Secret flux-system/ghcr-pull once Flux has rendered it
#            (within PULL_SECRET_LIMIT seconds, see pin_flux); waits for the template's marker
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
#            The kit charts of the change: first, with the plan of charts for --changed <file>
#            (without it, every chart is the published one), up writes LOCAL_PACK_DIR/kit-charts.txt
#            and LOCAL_PACK_DIR/tree-charts.tsv (the TREE releases). For those, in each region and
#            as soon as the template has applied the Kustomization sovereign-root, tree_redirect
#            makes bootstrap-kit give each of them its chart from the GitRepository
#            flux-system/openova, the commit under test, which source-controller packages from
#            the folder; and the wait above also waits until tree_judge finds each of them
#            installed from the tree (Ready where the region's core needs it; one installed
#            before the change of source gets one forced upgrade). The verdict of each, per
#            region, is appended to kit-charts.txt. A file named that does not exist stops up.
#            The catalyst-api image of the change: when api_plan says TREE, up builds the image
#            from this tree with API_CONTAINERFILE before the apply (api_build), named
#            API_REPO:tree-<the first 12 hex digits of the commit of the tree> and never pushed;
#            once the boot template is complete it loads it into the containerd of k3s on every
#            machine of the pack, each of which must hold it under one digest (api_load_pack); and
#            tree_redirect gives the console release the value images.catalystApi.tag of that name.
#            After the console's API is available, up waits until every pod of the Deployment
#            catalyst-system/catalyst-api of the primary region reports that digest as its
#            imageID (api_judge); without TREE it waits until they run and names the image and
#            imageID they run. A secondary region holds no catalyst-api (slot 13 suspends the
#            console release there): that is said, not judged. Each line goes to the log and to
#            LOCAL_PACK_DIR/api-image.txt.
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
#            a restart names its container and its cause. It never fails. It copies the kit
#            chart plan of up and, for the charts of the tree, the last verdicts and the
#            HelmRelease and HelmChart they were read from (with the error of a failed read), and
#            the catalyst-api image of up: api-image.txt, the build log and, per region, its last
#            verdict and the Deployment and pods it was read from.
#            The kubeconfigs, the session and the module inputs are not copied there.
# down       Stops the port forward, then removes what up started: tofu destroy of the machines,
#            docker volume rm of each volume of the pack (each within VOLUME_LIMIT seconds; the
#            provider allows a volume only 30), tofu destroy of the rest. Exit 0 when nothing was
#            started, or when after that no container, volume or network of the pack is left;
#            otherwise exit 1 naming what is left.
#
# LOCAL_PACK_DIR defaults to $RUNNER_TEMP/local-pack (CI) or /tmp/local-pack. Requires bash,
# python3 with PyYAML, docker with BuildKit (buildx), timeout, tofu, kubectl, yq (v4), curl and go;
# charts needs kubectl and yq; --self-test needs bash, timeout, python3, kubectl (its kustomize)
# and yq. The machines expose no node port: the gateway binds host ports on the machine
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
# The kit of the pack (charts in the header) and the registry its own charts are published to.
KIT_PATH="clusters/_template/providers/local/kit"
PUBLISHED_URL="oci://ghcr.io/openova-io"
# The image of catalyst-api, the console's API (the Deployment catalyst-system/catalyst-api of the
# console release): catalyst-build.yaml builds it with this Containerfile, the root of the tree as
# its build context, and pushes it to API_REPO; the chart of the console release names its tag
# (images.catalystApi.tag; the pack sets no global.imageRegistry). A build from the tree (up) has
# at most API_IMAGE_ATTEMPTS attempts of at most API_IMAGE_ATTEMPT_LIMIT seconds: the same build
# took 159 seconds in the job build-api of catalyst-build.yaml on 27 November (a download in it may
# stall, as in image). A machine's k3s has API_LOAD_LIMIT seconds to answer ctr before the load.
API_CONTAINERFILE="products/catalyst/bootstrap/api/Containerfile"
API_REPO="ghcr.io/openova-io/openova/catalyst-api"
API_IMAGE_ATTEMPTS=2
API_IMAGE_ATTEMPT_LIMIT=600
API_LOAD_LIMIT=300
CONSOLE_PORT=18080
SIGNER_DEFAULT="/var/lib/catalyst/handover-jwt-private.pem"
# The machine image (image): the build took 34 to 108 seconds on the CI runner until 19 November
# 06:10 UTC, then 280 seconds, 714 seconds and more than 20 minutes for the same input while a
# download inside it was slow. An attempt that has not finished in 300 seconds is stopped and
# the build starts again; the steps it finished stay in the build cache.
IMAGE_NAME="catalyst-local-machine"
IMAGE_ATTEMPTS=3
IMAGE_ATTEMPT_LIMIT=300
# down: the most seconds docker volume rm may take for one volume of the pack (the slowest
# measured took more than 50, see cmd_down).
VOLUME_LIMIT=300
MARKER_LIMIT=900
PULL_SECRET_LIMIT=600
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
usage() { sed -n '9,16p' "${BASH_SOURCE[0]}" | sed 's/^# //' >&2; exit 2; }

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
  local p
  p=$(pack_prefix "$1") || return 1
  echo "$p-$2-cp1"
}

# machines <module inputs>: the container of every machine of the pack, one per line, region by
# region: its control-plane machine, then its workers (the keys of local.control_planes and
# local.workers in infra/providers/local/main.tf). Exit 1 when the inputs cannot be read.
machines() {
  local p rows code n i
  p=$(pack_prefix "$1") || return 1
  rows=$(yq -p json -o tsv '.regions[] | [.code, .worker_count]' "$1") && [ -n "$rows" ] || return 1
  while IFS=$'\t' read -r code n; do
    [[ "$code" =~ ^[a-z0-9-]+$ ]] && [[ "$n" =~ ^[0-9]+$ ]] || return 1
    echo "$p-$code-cp1"
    for (( i = 1; i <= n; i++ )); do echo "$p-$code-w$i"; done
  done <<< "$rows"
}

# pack_prefix <file>: the start of the name of every container, volume and network of the pack,
# local.name_prefix of infra/providers/local/main.tf.
pack_prefix() {
  local fqdn id
  fqdn=$(tfvar "$1" sovereign_fqdn) && id=$(tfvar "$1" deployment_id) || return 1
  echo "catalyst-${fqdn//./-}-${id:0:8}"
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

# --- the kit charts of a run (see charts in the header) -------------------------------------

# kit_releases <kit folder>: one line per HelmRelease of the folder as kustomize-controller builds
# it (kubectl kustomize, no load restrictions), its fields separated by tabs:
#   <release> <namespace> <chart> <version> <source kind> <source name> <source url>
# The source is the object of the same build that spec.chart.spec.sourceRef names (in the
# release's namespace when the reference names none). A field the build does not hold is "-".
# Exit 1 when the build fails or holds no HelmRelease.
kit_releases() {
  local render src rel
  render=$(kubectl kustomize --load-restrictor LoadRestrictionsNone "$1") || return 1
  src=$(printf '%s\n' "$render" | yq -N 'select(.kind == "HelmRepository" or .kind == "GitRepository") | [[.kind, .metadata.namespace, .metadata.name, .spec.url] | join("|")] | @tsv') || return 1
  rel=$(printf '%s\n' "$render" | yq -N 'select(.kind == "HelmRelease") | [[.metadata.name, .metadata.namespace, .spec.chart.spec.chart, .spec.chart.spec.version, .spec.chart.spec.sourceRef.kind, .spec.chart.spec.sourceRef.namespace, .spec.chart.spec.sourceRef.name] | join("|")] | @tsv') || return 1
  awk -F'|' -v OFS='\t' '
    NR == FNR { if (NF == 4) url[$1 "|" $2 "|" $3] = $4; next }
    NF == 7 {
      u = url[$5 "|" ($6 == "" ? $2 : $6) "|" $7]
      for (i = 1; i <= 7; i++) if ($i == "") $i = "-"
      print $1, $2, $3, $4, $5, $7, (u == "" ? "-" : u); n++
    }
    END { exit n ? 0 : 1 }' <(printf '%s\n' "$src") <(printf '%s\n' "$rel")
}

# chart_folders <tree>: one line per chart folder that blueprint-release.yaml publishes from,
# platform/<name>/chart and products/<name>/chart: "<chart name><tab><folder>", the name by the
# release's own rule (the first line name: of Chart.yaml). Exit 1 when two folders hold a chart
# of one name.
chart_folders() {
  local out f
  out=$(cd "$1" && for f in platform/*/chart/Chart.yaml products/*/chart/Chart.yaml; do
    if [ -f "$f" ]; then printf '%s\t%s\n' "$(awk '/^name:/{print $2; exit}' "$f" | tr -d '"')" "${f%/Chart.yaml}"; fi
  done) || return 1
  f=$(printf '%s\n' "$out" | cut -f1 | sort | uniq -d)
  [ -z "$f" ] || { echo "chart_folders: more than one folder holds the chart $f" >&2; return 1; }
  printf '%s\n' "$out"
}

# chart_deps <tree> <folder>: the folders of the chart's file:// dependencies and of theirs, one
# per line, relative to the tree. Such a dependency (a library chart) is packaged into the chart.
chart_deps() {
  local root=$1 f rel d seen=" $2 "
  local -a queue=("$2")
  while [ "${#queue[@]}" -gt 0 ]; do
    f=${queue[0]}
    queue=("${queue[@]:1}")
    while IFS= read -r rel; do
      d=$(realpath -m --relative-to="$root" "$root/$f/$rel")
      case "$seen" in *" $d "*) continue ;; esac
      seen+="$d "
      queue+=("$d")
      echo "$d"
    done < <(sed -n "s|^ *repository: *[\"']\{0,1\}file://\([^\"' ]*\).*|\1|p" "$root/$f/Chart.yaml" 2> /dev/null)
  done
}

# kit_plan <tree> [<file of changed paths>]: one line per HelmRelease of the kit KIT_PATH of the
# tree, the seven fields of kit_releases and three more:
#   <origin>   tree       its source is a HelmRepository at PUBLISHED_URL (blueprint-release.yaml
#                         publishes there from main), a folder of the tree holds a chart of its
#                         name, and a changed path lies in that folder or in the folder of a
#                         file:// dependency of it: the run installs it from the tree
#              published  the same source, and no changed path selects it (or no file is given)
#              source     another source (the upstream GitRepository of local-path): never from
#                         the tree
#   <folder>   the folder of the chart in the tree, or -
#   <because>  the first changed path (in the order of the file) that selected it, or -
# The file holds one path per line, relative to the tree. Exit 1 when the file is named and does
# not exist, or the kit or the chart folders cannot be read.
kit_plan() {
  local root=$1 list=${2:-} rels folders rel ns chart ver kind name url origin folder because
  local -a paths=() deps=()
  local -A folder_of=()
  if [ -n "$list" ]; then
    [ -f "$list" ] || { echo "kit_plan: the file of changed paths $list does not exist" >&2; return 1; }
    mapfile -t paths < "$list"
  fi
  rels=$(kit_releases "$root/$KIT_PATH") || { echo "kit_plan: the kit $KIT_PATH cannot be built" >&2; return 1; }
  folders=$(chart_folders "$root") || return 1
  while IFS=$'\t' read -r chart folder; do folder_of[$chart]=$folder; done <<< "$folders"
  # first_changed <folder>...: the first changed path that lies in one of the folders.
  first_changed() {
    local p f
    for p in "${paths[@]}"; do
      for f in "$@"; do case "$p" in "$f"/*) echo "$p"; return 0 ;; esac; done
    done
    return 1
  }
  while IFS=$'\t' read -r rel ns chart ver kind name url; do
    origin=source folder=- because=-
    if [ "$kind" = HelmRepository ] && [ "$url" = "$PUBLISHED_URL" ]; then
      origin=published
      folder=${folder_of[$chart]:--}
      [ "$folder" = - ] || mapfile -t deps < <(chart_deps "$root" "$folder")
      if [ "$folder" != - ] && because=$(first_changed "$folder" "${deps[@]}"); then
        origin=tree
      else
        because=-
      fi
    fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$rel" "$ns" "$chart" "$ver" "$kind" "$name" "$url" "$origin" "$folder" "$because"
  done <<< "$rels"
}

# plan_report <file of changed paths or ''>: stdin has the lines of kit_plan; stdout says, one line
# per release, where the run takes its chart from.
plan_report() {
  awk -F'\t' -v list="$1" -v kit="$KIT_PATH" '
    { n++; if ($8 == "tree") t++
      if ($8 == "tree") run = "TREE ./" $9 " at the commit under test (changed: " $10 ")"
      else if ($8 == "published") run = "published " $7 "/" $3 ":" $4 (($9 == "-") ? " (no folder of the tree holds it)" : "")
      else run = "source " $5 " " $6 " " $7
      line[n] = sprintf("%-22s %-38s %s", $1, $3, run) }
    END {
      printf "The kit %s holds %d HelmReleases; %d take their chart from the tree of this change", kit, n, t
      print (list == "" ? " (no list of changed paths: every chart is the published one)." : ".")
      for (i = 1; i <= n; i++) print line[i] }'
}

# tree_patch: stdin has the lines of kit_plan; stdout the JSON patch of the Kustomization
# flux-system/sovereign-root that appends one entry to its spec.patches, for the lines whose origin
# is tree. The entry patches the Kustomization bootstrap-kit, which sovereign-root applies: its
# spec.patches get one patch per release, so that the release takes its chart from the tree,
#   spec.chart.spec: {chart: ./<folder>, version: '*', sourceRef: GitRepository flux-system/openova}
# The GitRepository is the one pin_flux pins to the commit under test. Flux ignores the version
# of a chart from a GitRepository, and '*' is what its API writes when none is named, so step
# D0.12 of the run in self does not compare it with the deployed one. The reconcile strategy
# stays ChartVersion: Revision would add +<commit> to the chart version, which a label value and
# the version pattern of the Blueprint and Application kinds refuse. Exit 1 when no line is tree,
# or a name or folder holds a character outside [a-z0-9./_-] (the flow mappings do not quote it).
# tree_patch <tag> <namespace>: the same, and one more patch gives the console release
# CONSOLE_RELEASE in <namespace> the value images.catalystApi.tag: <tag>, so that its Deployment
# catalyst-api runs API_REPO:<tag>, the image of the tree that up loads into every machine. Exit 1
# when the tag is not tree-<12 hex digits> (the only tag up makes; YAML reads it as a string) or
# the namespace is not a name; with a tag, a plan with no line of the tree is not refused.
tree_patch() {
  local tag=${1:-} tns=${2:-} rel ns chart ver kind name url origin folder because v entries=""
  while IFS=$'\t' read -r rel ns chart ver kind name url origin folder because; do
    [ "$origin" = tree ] || continue
    for v in "$rel" "$ns" "$folder"; do [[ "$v" =~ ^[a-z0-9][a-z0-9./_-]*$ ]] || return 1; done
    entries+="${entries:+, }{patch: '{apiVersion: helm.toolkit.fluxcd.io/v2, kind: HelmRelease, metadata: {name: $rel, namespace: $ns}, spec: {chart: {spec: {chart: ./$folder, version: ''*'', sourceRef: {kind: GitRepository, name: openova, namespace: flux-system}}}}}'}"
  done
  if [ -n "$tag" ]; then
    [[ "$tag" =~ ^tree-[0-9a-f]{12}$ ]] && [[ "$tns" =~ ^[a-z0-9][a-z0-9-]*$ ]] || return 1
    entries+="${entries:+, }{patch: '{apiVersion: helm.toolkit.fluxcd.io/v2, kind: HelmRelease, metadata: {name: $CONSOLE_RELEASE, namespace: $tns}, spec: {values: {images: {catalystApi: {tag: $tag}}}}}'}"
  fi
  [ -n "$entries" ] || return 1
  printf '[{"op":"add","path":"/spec/patches/-","value":{"patch":"{apiVersion: kustomize.toolkit.fluxcd.io/v1, kind: Kustomization, metadata: {name: bootstrap-kit, namespace: flux-system}, spec: {patches: [%s]}}"}}]\n' "$entries"
}

# jfields <json file> <expression>...: the values of the expressions in one JSON document,
# joined by "|"; a value the document does not hold is empty.
jfields() {
  local f=$1 e expr=""
  shift
  for e in "$@"; do expr+="${expr:+, }$e"; done
  yq -p json -o tsv "[[$expr] | join(\"|\")]" "$f"
}

# tree_judge <HelmRelease json> <HelmChart json> <chart> <folder> <commit> <required: yes|no>:
# whether a release that the run installs from the tree did so. The HelmChart is the one
# status.helmChart of the release names ({} when there is none). Prints one line; exit
#   0  suspended (not judged); or installed from the tree; or, when not required, the release
#      names the tree and was not installed yet, or its release of the tree is not Ready
#   1  waiting: the release or its HelmChart does not name the tree yet, the HelmChart is not
#      built from <folder> at <commit>, or (required) the release of the tree is not Ready
#   3  stale: the newest release (history[0]) was deployed no later than the HelmChart's artifact
#      of the tree was built (the same second counts as before), or is another chart or version,
#      and helm-controller has handled the current generation and is not progressing: an install
#      of the published chart before the change of source. A chart version that did not change
#      gives helm-controller no reason to upgrade, so the caller forces the upgrade once.
# Installed from the tree: the newest release is <chart> at the version of the HelmChart's
# artifact, deployed after it was built, status deployed, and the release is Ready at its
# current generation.
tree_judge() {
  local chart=$3 folder=$4 commit=$5 required=$6
  local suspend gen ogen kind name path ready reason hname hver hstatus hdeployed
  local ckind cname cpath csrc cver ctime cready deployed built
  IFS='|' read -r suspend gen ogen kind name path ready reason hname hver hstatus hdeployed < <(jfields "$1" \
    .spec.suspend .metadata.generation .status.observedGeneration .spec.chart.spec.sourceRef.kind \
    .spec.chart.spec.sourceRef.name .spec.chart.spec.chart '([.status.conditions[] | select(.type == "Ready")][0].status)' \
    '([.status.conditions[] | select(.type == "Ready")][0].reason)' .status.history[0].chartName \
    .status.history[0].chartVersion .status.history[0].status .status.history[0].lastDeployed)
  IFS='|' read -r ckind cname cpath csrc cver ctime cready < <(jfields "$2" .spec.sourceRef.kind .spec.sourceRef.name \
    .spec.chart .status.observedSourceArtifactRevision .status.artifact.revision .status.artifact.lastUpdateTime \
    '([.status.conditions[] | select(.type == "Ready")][0].status)')
  if [ "$suspend" = true ]; then echo "suspended, not judged"; return 0; fi
  if [ "$kind/$name" != GitRepository/openova ] || [ "$path" != "./$folder" ]; then
    echo "waiting: the HelmRelease takes its chart from ${kind:-?}/${name:-?} ${path:-?}, not ./$folder of the tree"; return 1
  fi
  if [ "$ckind/$cname" != GitRepository/openova ] || [ "$cpath" != "./$folder" ] || [[ "$csrc" != *"@sha1:$commit" ]] \
    || [ "$cready" != True ] || [ -z "$cver" ] || ! built=$(date -u -d "$ctime" +%s 2> /dev/null); then
    echo "waiting: its HelmChart is not built from ./$folder at $commit (source ${ckind:-?}/${cname:-?} ${cpath:-?} at ${csrc:-?}, Ready=${cready:-?})"; return 1
  fi
  if [ -z "$hver" ]; then
    if [ "$required" = yes ]; then echo "waiting: not installed yet (Ready=${ready:-?} ${reason:-})"; return 1; fi
    echo "names the tree, not installed (Ready=${ready:-?} ${reason:-})"; return 0
  fi
  deployed=$(date -u -d "$hdeployed" +%s 2> /dev/null || echo 0)
  if [ "$deployed" -le "$built" ] || [ "$hname" != "$chart" ] || [ "$hver" != "$cver" ]; then
    if [ "$ogen" = "$gen" ] && [ -n "$ready" ] && [ "$ready" != Unknown ]; then
      echo "stale: the newest release is $hname $hver deployed $hdeployed, not after the chart of the tree $cver was built ($ctime)"; return 3
    fi
    echo "waiting: the newest release is $hname $hver deployed $hdeployed, the chart of the tree $cver was built $ctime (Ready=${ready:-?} ${reason:-})"; return 1
  fi
  if [ "$hstatus" = deployed ] && [ "$ready" = True ] && [ "$ogen" = "$gen" ]; then
    echo "installed from the tree: $hname $hver of ./$folder at $commit, deployed $hdeployed, Ready"; return 0
  fi
  if [ "$required" = yes ]; then echo "waiting: $hname $hver of the tree is $hstatus, Ready=${ready:-?} ${reason:-}"; return 1; fi
  echo "installed from the tree: $hname $hver of ./$folder at $commit, $hstatus, Ready=${ready:-?} ${reason:-}"
}

# --- the catalyst-api image of a run (see charts and up in the header) ----------------------------

# api_sources <tree>: the paths the image of catalyst-api is built from, one per line, sorted:
# API_CONTAINERFILE and the source of every COPY of it from the build context (not --from another
# stage), relative to the root of the tree, the build context of catalyst-build.yaml; a folder
# without its trailing slash. Exit 1 when the Containerfile does not exist, copies nothing from
# the build context, or holds a COPY or ADD that this rule does not read (an ADD, the JSON form, a
# continued line, a pattern, the whole context, a path outside the tree): then which paths build
# the image is not known.
api_sources() {
  local f="$1/$API_CONTAINERFILE" line s i from out=$API_CONTAINERFILE n=0
  local -a w
  [ -f "$f" ] || { echo "api_sources: $API_CONTAINERFILE does not exist" >&2; return 1; }
  while IFS= read -r line; do
    read -r -a w <<< "$line"
    if [ "${w[0]^^}" != COPY ] || [[ "$line" == *\\ ]]; then
      echo "api_sources: $API_CONTAINERFILE: a line this rule does not read: $line" >&2; return 1
    fi
    i=1 from=no
    while [ "$i" -lt "${#w[@]}" ] && [[ "${w[$i]}" == --* ]]; do
      [[ "${w[$i]}" != --from=* ]] || from=yes
      i=$((i + 1))
    done
    [ "$from" = no ] || continue
    [ $(( ${#w[@]} - i )) -ge 2 ] || { echo "api_sources: $API_CONTAINERFILE: a COPY without a source: $line" >&2; return 1; }
    for (( ; i < ${#w[@]} - 1; i++ )); do
      s=${w[$i]#./}
      s=${s%/}
      if ! [[ "$s" =~ ^[A-Za-z0-9_][A-Za-z0-9._/-]*$ ]] || [[ "/$s/" == */../* ]]; then
        echo "api_sources: $API_CONTAINERFILE: a source this rule does not read: ${w[$i]} in: $line" >&2; return 1
      fi
      out+=$'\n'"$s" n=$((n + 1))
    done
  done < <(grep -i -E '^[[:space:]]*(COPY|ADD)[[:space:]]' "$f")
  [ "$n" -gt 0 ] || { echo "api_sources: $API_CONTAINERFILE copies nothing from the build context" >&2; return 1; }
  printf '%s\n' "$out" | LC_ALL=C sort -u
}

# api_plan <tree> [<file of changed paths>]: where the console's catalyst-api image comes from, one
# line "<origin><tab><because>":
#   tree    and the first changed path (in the order of the file) that is a path of api_sources or
#           lies in one of its folders: up builds the image from the tree and the console runs it
#   pinned  and -: no changed path is one, or no file is given: the console runs the image that
#           the chart of the console release names
# The file holds one path per line, relative to the tree. Exit 1 when the file is named and does
# not exist, or api_sources fails.
api_plan() {
  local root=$1 list=${2:-} srcs p s
  srcs=$(api_sources "$root") || return 1
  if [ -n "$list" ]; then
    [ -f "$list" ] || { echo "api_plan: the file of changed paths $list does not exist" >&2; return 1; }
    while IFS= read -r p || [ -n "$p" ]; do
      while IFS= read -r s; do
        case "$p" in "$s" | "$s"/*) printf 'tree\t%s\n' "$p"; return 0 ;; esac
      done <<< "$srcs"
    done < "$list"
  fi
  printf 'pinned\t-\n'
}

# api_report <line of api_plan> <file of changed paths or ''>: one line, where the console's
# catalyst-api image comes from.
api_report() {
  local origin because
  IFS=$'\t' read -r origin because <<< "$1"
  if [ "$origin" = tree ]; then
    echo "catalyst-api: TREE, the image built with $API_CONTAINERFILE from the tree of this change (changed: $because)"
  elif [ -n "$2" ]; then
    echo "catalyst-api: pinned, the image the chart of $CONSOLE_RELEASE names (no changed path is a path the image is built from)"
  else
    echo "catalyst-api: pinned, the image the chart of $CONSOLE_RELEASE names (no list of changed paths)"
  fi
}

# api_judge <Deployment json> <pods json> <ref or -> <digest or -> <required: yes|no>: whether the
# Deployment catalyst-system/catalyst-api of a region runs the image it must. The pods are those of
# its ReplicaSets (owner catalyst-api-<hash>) that are not being deleted. Prints one line; exit
#   0  there is no Deployment ({}) and it is not required: a secondary region, where slot 13
#      suspends the console release; or it runs: it names <ref> (with -, any image: the pinned
#      one), has handled its generation, every replica is updated and available, it has exactly
#      that many pods, and the container catalyst-api of each is Ready with the imageID
#      <repository of ref>@<digest> (with -, any imageID, which is named)
#   1  waiting: anything else
api_judge() {
  local ref=$3 digest=$4 required=$5 name gen ogen rep upd avail image rows pod ready id n=0 names="" ids=""
  IFS='|' read -r name gen ogen rep upd avail image < <(jfields "$1" .metadata.name .metadata.generation \
    .status.observedGeneration .spec.replicas .status.updatedReplicas .status.availableReplicas \
    '([.spec.template.spec.containers[] | select(.name == "catalyst-api")][0].image)')
  if [ -z "$name" ]; then
    if [ "$required" = yes ]; then echo "waiting: no Deployment catalyst-system/catalyst-api"; return 1; fi
    echo "no Deployment catalyst-system/catalyst-api in this region (the console release runs in the primary region), not judged"; return 0
  fi
  if [ "$ref" != - ] && [ "$image" != "$ref" ]; then
    echo "waiting: the Deployment names ${image:-no container catalyst-api}, not $ref"; return 1
  fi
  if ! [[ "$rep" =~ ^[1-9][0-9]*$ ]] || [ "$ogen" != "$gen" ] || [ "$upd" != "$rep" ] || [ "$avail" != "$rep" ]; then
    echo "waiting: the Deployment of $image has handled generation ${ogen:-?} of ${gen:-?}; ${upd:-0} updated and ${avail:-0} available of ${rep:-?}"; return 1
  fi
  rows=$(yq -p json -o tsv '.items[] | select(.metadata.deletionTimestamp == null and (.metadata.ownerReferences[0].name // "" | test("^catalyst-api-[a-z0-9]+$")))
      | [[.metadata.name, ([.status.containerStatuses[] | select(.name == "catalyst-api")][0].ready | tostring),
          ([.status.containerStatuses[] | select(.name == "catalyst-api")][0].imageID // "")] | join("|")]' "$2") \
    || { echo "waiting: the pods of the Deployment cannot be read"; return 1; }
  while IFS='|' read -r pod ready id; do
    [ -n "$pod" ] || continue
    n=$((n + 1)) names+="${names:+ }$pod"
    if [ "$ready" != true ] || [ -z "$id" ] || { [ "$digest" != - ] && [ "$id" != "${ref%:*}@$digest" ]; }; then
      echo "waiting: the pod $pod runs ${id:-no image yet} (Ready=$ready)$([ "$digest" = - ] || echo ", not ${ref%:*}@$digest")"; return 1
    fi
    case " $ids " in *" $id "*) ;; *) ids+="${ids:+ }$id" ;; esac
  done <<< "$rows"
  if [ "$n" != "$rep" ]; then echo "waiting: the Deployment has $n pod(s) of $rep replica(s): ${names:-none}"; return 1; fi
  if [ "$ref" = - ]; then echo "runs the pinned image $image as $ids: $n pod(s) ($names), Ready"; return 0; fi
  echo "runs $ref of the tree as ${ref%:*}@$digest: $n pod(s) ($names), Ready"
}

# --- commands -------------------------------------------------------------------------------

kin() { local m=$1; shift; docker exec -i "$m" kubectl --kubeconfig /etc/rancher/k3s/k3s.yaml --request-timeout=20s "$@"; }

# tofu_run <command> <argument>...: tofu in the module, its working data under LOCAL_PACK_DIR.
tofu_run() {
  local sub=$1
  shift
  TF_DATA_DIR="$DIR/tofu-data" tofu -chdir="$MODULE" "$sub" -no-color "$@"
}

# pin_flux <region> <machine> <branch> <commit>: the commit and the pull credential. Returns 1
# while the template has not applied the Flux objects yet. Then it pins the GitRepository, as
# before, and waits for the Secret flux-system/ghcr-pull: first boot no longer writes it, the
# Kustomization bootstrap-secrets renders it from the seed (SEC-154), once
# (kustomize.toolkit.fluxcd.io/ssa: IfNotPresent), so the credential patched here stays. A Secret
# that is not there PULL_SECRET_LIMIT seconds after the Flux objects, or a patch that fails,
# stops the run naming it; the log line is printed only after both patches succeeded.
pin_flux() {
  local code=$1 m=$2 branch=$3 commit=$4 auth cfg t
  kin "$m" -n flux-system get kustomization bootstrap-kit > /dev/null 2>&1 || return 1
  printf '{"spec":{"ref":{"branch":"%s","commit":"%s"}}}' "$branch" "$commit" \
    | kin "$m" -n flux-system patch gitrepository openova --type merge --patch-file /dev/stdin > /dev/null \
    || die "$code: the patch of the GitRepository flux-system/openova failed"
  t=$(date +%s)
  until kin "$m" -n flux-system get secret ghcr-pull > /dev/null 2>&1; do
    [ $(( $(date +%s) - t )) -lt "$PULL_SECRET_LIMIT" ] \
      || die "$code: the Secret flux-system/ghcr-pull does not exist ${PULL_SECRET_LIMIT}s after the Flux objects (the Kustomization bootstrap-secrets renders it from the seed)"
    sleep 2
  done
  auth=$(printf '%s:%s' "$GHCR_USER" "$GHCR_TOKEN" | base64 -w0)
  cfg=$(printf '{"auths":{"ghcr.io":{"username":"%s","password":"%s","auth":"%s"}}}' "$GHCR_USER" "$GHCR_TOKEN" "$auth" | base64 -w0)
  printf '{"data":{".dockerconfigjson":"%s"}}' "$cfg" \
    | kin "$m" -n flux-system patch secret ghcr-pull --type merge --patch-file /dev/stdin > /dev/null \
    || die "$code: the patch of the Secret flux-system/ghcr-pull failed"
  log "$code: pull credential set, GitRepository at $commit"
}

# tree_redirect <region> <machine>: makes the region install the kit charts of the tree (the
# lines of LOCAL_PACK_DIR/tree-charts.tsv) from the GitRepository flux-system/openova. Returns 1
# while the template has not applied the Kustomization sovereign-root yet. Then it appends the
# entry of tree_patch to sovereign-root's spec.patches; nothing applies sovereign-root after first
# boot, so the entry stays, and every apply of bootstrap-kit carries it. It runs as soon as
# sovereign-root exists, before Flux has the source to build bootstrap-kit from, so a release is
# normally created with the tree as its source; one that was created and installed before is
# found by tree_judge and upgraded. A patch that fails stops the run naming sovereign-root.
# When LOCAL_PACK_DIR/api-tag holds "<tag> <namespace>" (up: the catalyst-api image of the tree),
# the same entry also gives the console release that image (tree_patch <tag> <namespace>).
tree_redirect() {
  local code=$1 m=$2 before=no tag="" tns="" what
  kin "$m" -n flux-system get kustomization sovereign-root > /dev/null 2>&1 || return 1
  if kin "$m" -n flux-system get kustomization bootstrap-kit > /dev/null 2>&1; then before=yes; fi
  if [ -s "$DIR/api-tag" ]; then read -r tag tns < "$DIR/api-tag"; fi
  tree_patch ${tag:+"$tag" "$tns"} < "$DIR/tree-charts.tsv" \
    | kin "$m" -n flux-system patch kustomization sovereign-root --type json --patch-file /dev/stdin > /dev/null \
    || die "$code: the patch of the Kustomization flux-system/sovereign-root failed"
  what="$(cut -f1 "$DIR/tree-charts.tsv" | tr '\n' ' ')from the tree"
  [ -n "$tag" ] || { log "$code: bootstrap-kit takes $what (sovereign-root patched; bootstrap-kit existed before: $before)"; return 0; }
  [ -s "$DIR/tree-charts.tsv" ] || what="no chart from the tree"
  log "$code: bootstrap-kit takes $what, and $CONSOLE_RELEASE runs the catalyst-api image $API_REPO:$tag (sovereign-root patched; bootstrap-kit existed before: $before)"
}

# tree_states <region> <machine> <releases required Ready> <commit>: judges each release of
# LOCAL_PACK_DIR/tree-charts.tsv in the region with tree_judge (required: it is one of the named
# releases), writes "<release>: <verdict>" per release to LOCAL_PACK_DIR/tree/<region>.txt and
# prints the verdicts that are not done. A stale release gets one forced upgrade
# (reconcile.fluxcd.io/requestedAt and forceAt, as flux reconcile --force sets them); a request
# that fails stops the run. Exit 0 when every release is done.
# The HelmChart is read as helmcharts.source.toolkit.fluxcd.io: on k3s the short name helmchart
# is the kind HelmChart of helm.cattle.io (the Helm controller of k3s), which holds no HelmChart
# of Flux. The error of a read that fails is kept beside it in <region>-<release>-helmchart.err.
tree_states() {
  local code=$1 m=$2 required=" $3 " commit=$4 rel ns chart ver kind name url origin folder because
  local hr hc ref req v st rc=0 tok
  : > "$DIR/tree/$code.txt"
  while IFS=$'\t' read -r rel ns chart ver kind name url origin folder because; do
    hr="$DIR/tree/$code-$rel-helmrelease.json" hc="$DIR/tree/$code-$rel-helmchart.json"
    # kubectl reads no stdin here: docker exec -i would otherwise take the rest of the list.
    kin "$m" -n "$ns" get helmrelease "$rel" -o json < /dev/null > "$hr" 2> /dev/null || echo '{}' > "$hr"
    ref=$(jfields "$hr" .status.helmChart)
    echo '{}' > "$hc"
    if [ -n "$ref" ]; then
      kin "$m" -n "${ref%%/*}" get helmcharts.source.toolkit.fluxcd.io "${ref#*/}" -o json < /dev/null > "$hc" 2> "${hc%.json}.err" \
        || echo '{}' > "$hc"
    fi
    req=no
    case "$required" in *" $rel "*) req=yes ;; esac
    st=0
    v=$(tree_judge "$hr" "$hc" "$chart" "$folder" "$commit" "$req") || st=$?
    echo "$rel: $v" >> "$DIR/tree/$code.txt"
    if [ "$st" = 3 ] && [ ! -f "$DIR/tree/$code-$rel-forced" ]; then
      tok="local-pack-$(date -u +%s)"
      kin "$m" -n "$ns" annotate helmrelease "$rel" --overwrite "reconcile.fluxcd.io/requestedAt=$tok" \
        "reconcile.fluxcd.io/forceAt=$tok" < /dev/null > /dev/null \
        || die "$code: the forced upgrade of the HelmRelease $ns/$rel could not be requested"
      echo "$tok" > "$DIR/tree/$code-$rel-forced"
      log "$code: $rel: $v; forced upgrade $tok requested" >&2
    fi
    if [ "$st" != 0 ]; then rc=1; echo "$rel: $v"; fi
  done < "$DIR/tree-charts.tsv"
  return "$rc"
}

# tree_summary <region code>...: appends the verdicts of each region to LOCAL_PACK_DIR/kit-charts.txt.
tree_summary() {
  local code
  [ -s "$DIR/tree-charts.tsv" ] || return 0
  for code in "$@"; do
    { echo "$code:"; sed 's/^/  /' "$DIR/tree/$code.txt" 2> /dev/null || echo "  not judged"; } >> "$DIR/kit-charts.txt"
  done
}

# api_build <ref> <commit>: builds the image of catalyst-api from the tree ROOT as catalyst-build.yaml
# builds it (the root as the build context, API_CONTAINERFILE, BUILD_SHA the short commit of the
# tree, BUILD_TIME now), tagged <ref>, never pushed; without provenance and SBOM attestations, as
# the suite image of the run in self of conformance-local.yaml is built for ctr import. An image of
# that name already on this host is used as it is. Every build step and its time go to the log and
# LOCAL_PACK_DIR/api-build.log.
# Exit 1 when none of the API_IMAGE_ATTEMPTS attempts (each stopped after API_IMAGE_ATTEMPT_LIMIT
# seconds) built it.
api_build() {
  local ref=$1 commit=$2 a t0 rc=1
  if docker image inspect "$ref" > /dev/null 2>&1; then log "api image: $ref is on this host already, not built again"; return 0; fi
  for a in $(seq 1 "$API_IMAGE_ATTEMPTS"); do
    log "api image: build $ref with $API_CONTAINERFILE from the tree at $commit, attempt $a of $API_IMAGE_ATTEMPTS, stopped after ${API_IMAGE_ATTEMPT_LIMIT}s"
    t0=$(date +%s)
    rc=0
    DOCKER_BUILDKIT=1 timeout --kill-after=20 "$API_IMAGE_ATTEMPT_LIMIT" docker build --progress=plain --provenance=false --sbom=false --file "$ROOT/$API_CONTAINERFILE" \
      --build-arg "BUILD_SHA=${commit:0:7}" --build-arg "BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)" --tag "$ref" "$ROOT" \
      2>&1 | tee "$DIR/api-build.log" || rc=$?
    if [ "$rc" = 0 ]; then log "api image: $ref built in $(($(date +%s) - t0))s, attempt $a"; return 0; fi
    log "api image: attempt $a ended with exit code $rc after $(($(date +%s) - t0))s (124: stopped at the limit);" \
      "the last step it reached: $(grep -E '^#[0-9]+ \[' "$DIR/api-build.log" | tail -1 || true)"
  done
  return 1
}

# api_load <machine> <ref> <archive>: imports the image <ref> (the docker save of it in <archive>)
# into the containerd of k3s on the machine and makes its reference by digest <repository>@<digest>
# with ctr images tag, as the run in self of conformance-local.yaml does (the import's own
# --digests makes none for a named image in the containerd of k3s v1.31.8), so that a pod that runs
# it reports that reference as its imageID. Prints the digest. Exit 1, naming the step, when the
# import fails or the machine then holds no digest or no reference by digest for it.
api_load() {
  local m=$1 ref=$2 repo=${2%:*} got refs
  docker exec -i "$m" /usr/local/bin/k3s ctr -n k8s.io images import --all-platforms --base-name "$repo" - < "$3" > /dev/null \
    || { echo "api_load: $m did not import $ref" >&2; return 1; }
  got=$(docker exec "$m" /usr/local/bin/k3s ctr -n k8s.io images ls "name==$ref" < /dev/null | awk -v ref="$ref" '$1 == ref {print $3}')
  [[ "$got" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "api_load: $m holds no digest for $ref: '$got'" >&2; return 1; }
  docker exec "$m" /usr/local/bin/k3s ctr -n k8s.io images tag --force "$ref" "$repo@$got" < /dev/null > /dev/null \
    || { echo "api_load: $m could not tag $ref as $repo@$got" >&2; return 1; }
  refs=$(docker exec "$m" /usr/local/bin/k3s ctr -n k8s.io images ls -q < /dev/null | grep -c -x -F -- "$repo@$got" || true)
  [ "$refs" = 1 ] || { echo "api_load: $m has no reference $repo@$got" >&2; return 1; }
  echo "$got"
}

# api_load_pack <module inputs> <ref>: loads the image <ref> into every machine of the pack
# (machines): waits until the machine's k3s answers ctr (within API_LOAD_LIMIT seconds), then
# api_load. Every machine must hold it under one digest, which goes to LOCAL_PACK_DIR/api-digest;
# one line per machine goes to the log and to api-image.txt. A machine that does not answer, does
# not load it or holds another digest stops the run, naming it.
api_load_pack() {
  local ref=$2 all m t got digest="" tar="$DIR/api-image.tar"
  all=$(machines "$1") || die "api image: the machines of the pack cannot be named from $1"
  docker save "$ref" > "$tar" || die "api image: docker save of $ref failed"
  while IFS= read -r m; do
    t=$(date +%s)
    until docker exec "$m" /usr/local/bin/k3s ctr -n k8s.io images ls -q < /dev/null > /dev/null 2>&1; do
      [ $(( $(date +%s) - t )) -lt "$API_LOAD_LIMIT" ] || die "api image: the k3s of $m does not answer ctr within ${API_LOAD_LIMIT}s"
      sleep 5
    done
    got=$(api_load "$m" "$ref" "$tar") || die "api image: $m did not load $ref"
    [ -z "$digest" ] || [ "$got" = "$digest" ] || die "api image: $m holds $ref as $got, another machine as $digest"
    digest=$got
    log "api image: $m holds $ref as ${ref%:*}@$digest"
    echo "  $m holds it as ${ref%:*}@$digest" >> "$DIR/api-image.txt"
  done <<< "$all"
  rm -f "$tar"
  echo "$digest" > "$DIR/api-digest"
}

# api_states <region> <machine> <ref or -> <digest or -> <required: yes|no>: reads the Deployment
# catalyst-system/catalyst-api and its pods in the region, judges them with api_judge, writes the
# verdict to LOCAL_PACK_DIR/api/<region>-api.txt and prints it; exit as api_judge. A Deployment the
# API server says is NotFound is {}; any other failed read waits, its error kept in the .err file
# beside the JSON.
api_states() {
  local code=$1 m=$2 d="$DIR/api/$1-api-deployment.json" p="$DIR/api/$1-api-pods.json" v st=0
  if ! kin "$m" -n catalyst-system get deployment catalyst-api -o json < /dev/null > "$d" 2> "${d%.json}.err"; then
    if grep -q -F '(NotFound)' "${d%.json}.err"; then echo '{}' > "$d"; else
      v="waiting: the Deployment catalyst-system/catalyst-api cannot be read: $(tr '\n' ' ' < "${d%.json}.err")"
      echo "$v" | tee "$DIR/api/$code-api.txt"; return 1
    fi
  fi
  kin "$m" -n catalyst-system get pods -l app.kubernetes.io/name=catalyst-api -o json < /dev/null > "$p" 2> "${p%.json}.err" \
    || echo '{"items": []}' > "$p"
  v=$(api_judge "$d" "$p" "$3" "$4" "$5") || st=$?
  echo "$v" | tee "$DIR/api/$code-api.txt"
  return "$st"
}

# cmd_charts [--changed <file>]: see charts in the header.
cmd_charts() {
  local changed=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --changed) changed="${2:-}"; [ -n "$changed" ] || usage; shift 2 ;;
      *) usage ;;
    esac
  done
  local plan api
  plan=$(kit_plan "$ROOT" "$changed") || die "charts: the kit chart plan could not be made"
  api=$(api_plan "$ROOT" "$changed") || die "charts: the catalyst-api image plan could not be made"
  plan_report "$changed" <<< "$plan"
  api_report "$api" "$changed"
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
  local regions="" branch="" commit="" changed="" changed_set=no
  while [ $# -gt 0 ]; do
    case "$1" in
      --regions) regions="${2:-}"; shift 2 ;;
      --branch) branch="${2:-}"; shift 2 ;;
      --commit) commit="${2:-}"; shift 2 ;;
      --changed) changed="${2:-}"; changed_set=yes; shift 2 ;;
      *) usage ;;
    esac
  done
  [ "$changed_set" = no ] || [ -n "$changed" ] || usage
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
  mkdir -p "$DIR/tree"
  # The kit charts of this run: the ones the change touches come from the tree (charts above).
  kit_plan "$ROOT" "$changed" > "$DIR/kit-plan.tsv" || die "the kit chart plan could not be made"
  awk -F'\t' '$8 == "tree"' "$DIR/kit-plan.tsv" > "$DIR/tree-charts.tsv"
  plan_report "$changed" < "$DIR/kit-plan.tsv" > "$DIR/kit-charts.txt"
  cat "$DIR/kit-charts.txt"
  # The catalyst-api image of this run: from the tree when the change touches a path it is built
  # from (charts above), otherwise the pinned one.
  local api api_origin api_commit="" api_ref=- api_digest=- api_ns
  mkdir -p "$DIR/api"
  : > "$DIR/api-tag"
  api=$(api_plan "$ROOT" "$changed") || die "the catalyst-api image plan could not be made"
  IFS=$'\t' read -r api_origin _ <<< "$api"
  api_report "$api" "$changed" > "$DIR/api-image.txt"
  if [ "$api_origin" = tree ]; then
    api_commit=$(git -C "$ROOT" rev-parse HEAD) || die "the commit of the tree $ROOT cannot be read: the catalyst-api image of the tree has no name"
    api_ref="$API_REPO:tree-${api_commit:0:12}"
    api_ns=$(awk -F'\t' -v r="$CONSOLE_RELEASE" '$1 == r {print $2}' "$DIR/kit-plan.tsv")
    [ -n "$api_ns" ] || die "the kit holds no release $CONSOLE_RELEASE: nothing would run the catalyst-api image of the tree"
    echo "tree-${api_commit:0:12} $api_ns" > "$DIR/api-tag"
  fi
  cat "$DIR/api-image.txt"
  local vars="$DIR/pack.tfvars.json" dep="${commit:0:16}" t0 code m pcode pm image
  image=$(cmd_image)
  if [ "$api_origin" = tree ]; then
    t0=$(date +%s)
    api_build "$api_ref" "$api_commit" \
      || die "api image: $api_ref did not build in $API_IMAGE_ATTEMPTS attempts of at most ${API_IMAGE_ATTEMPT_LIMIT}s"
    echo "  image $api_ref of the tree at $api_commit, docker image id $(docker image inspect --format '{{.Id}}' "$api_ref"), ready after $(( $(date +%s) - t0 ))s, never pushed" >> "$DIR/api-image.txt"
  fi
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

  declare -A pinned=() done_=() redirected=()
  while :; do
    local all=yes
    for code in "${codes[@]}"; do
      m=$(machine "$vars" "$code")
      if docker exec "$m" sh -c 'ls /var/lib/catalyst/*-FAILED' > /dev/null 2>&1; then
        docker exec "$m" sh -c 'ls /var/lib/catalyst/*-FAILED; tail -60 /var/log/cloud-init-output.log' || true
        die "$code: the boot template failed"
      fi
      if { [ -s "$DIR/tree-charts.tsv" ] || [ -s "$DIR/api-tag" ]; } && [ -z "${redirected[$code]:-}" ]; then
        if tree_redirect "$code" "$m"; then redirected[$code]=1; else all=no; fi
      fi
      if [ -z "${pinned[$code]:-}" ]; then
        if pin_flux "$code" "$m" "$branch" "$commit"; then pinned[$code]=1; fi
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
  # The catalyst-api image of the tree goes into every machine before the console release installs.
  if [ "$api_origin" = tree ]; then
    api_load_pack "$vars" "$api_ref"
    api_digest=$(cat "$DIR/api-digest")
  fi

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
      # The kit charts of the tree: installed from it, Ready where the core needs them.
      if [ -s "$DIR/tree-charts.tsv" ] && ! tree_states "$code" "$m" "$(core_for "$code" "$pcode")" "$commit" > "$DIR/tree/$code-waiting.txt"; then
        all=no
        if [ $(( $(date +%s) - last )) -ge 120 ]; then log "$code: kit charts of the tree: $(tr '\n' ';' < "$DIR/tree/$code-waiting.txt")"; fi
      fi
    done
    [ $(( $(date +%s) - last )) -ge 120 ] && last=$(date +%s)
    [ "$all" = yes ] && break
    if [ $(( $(date +%s) - t0 )) -ge "$CORE_LIMIT" ]; then
      report_state "$vars" "${others[@]}" "$pcode"
      [ -s "$DIR/tree-charts.tsv" ] || die "the kit core was not Ready within ${CORE_LIMIT}s"
      tree_summary "${codes[@]}"
      for code in "${codes[@]}"; do log "$code: kit charts of the tree: $(tr '\n' ';' < "$DIR/tree/$code.txt")"; done
      die "the kit core, or a kit chart of the tree, was not Ready within ${CORE_LIMIT}s"
    fi
    sleep 15
  done
  log "kit core Ready in every region after $(( $(date +%s) - t0 ))s"
  if [ -s "$DIR/tree-charts.tsv" ]; then
    tree_summary "${codes[@]}"
    for code in "${codes[@]}"; do log "$code: kit charts of the tree: $(tr '\n' ';' < "$DIR/tree/$code.txt")"; done
  fi
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
  # The image the console's API runs, in every region (api_judge; required in the primary region).
  local req aok alast=0
  while :; do
    aok=yes
    for code in "${codes[@]}"; do
      req=no
      [ "$code" != "$pcode" ] || req=yes
      api_states "$code" "$(machine "$vars" "$code")" "$api_ref" "$api_digest" "$req" > /dev/null || aok=no
    done
    [ "$aok" = yes ] && break
    if [ $(( $(date +%s) - t0 )) -ge "$CORE_LIMIT" ]; then
      for code in "${codes[@]}"; do log "$code: catalyst-api: $(cat "$DIR/api/$code-api.txt")"; done
      report_state "$vars" "$pcode"
      die "$pcode: catalyst-system/catalyst-api did not run $([ "$api_ref" = - ] && echo "its pinned image" || echo "$api_ref of the tree") within ${CORE_LIMIT}s"
    fi
    if [ $(( $(date +%s) - alast )) -ge 120 ]; then
      for code in "${codes[@]}"; do log "$code: catalyst-api: $(cat "$DIR/api/$code-api.txt")"; done
      alast=$(date +%s)
    fi
    sleep 10
  done
  for code in "${codes[@]}"; do
    log "$code: catalyst-api: $(cat "$DIR/api/$code-api.txt")"
    echo "$code: $(cat "$DIR/api/$code-api.txt")" >> "$DIR/api-image.txt"
  done

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
    kin "$m" get kustomization,gitrepository,helmrepository,helmcharts.source.toolkit.fluxcd.io -A > "$d/flux.txt" 2>&1 || true
    kin "$m" get storageclass,pvc -A > "$d/storage.txt" 2>&1 || true
    kin "$m" get events -A --field-selector type=Warning --sort-by=.lastTimestamp > "$d/warnings.txt" 2>&1 || true
    kin "$m" describe nodes > "$d/nodes-describe.txt" 2>&1 || true
    kin "$m" -n catalyst-system get pods -o wide > "$d/console-pods.txt" 2>&1 || true
    kin "$m" -n catalyst-system logs deployment/catalyst-api --tail=400 > "$d/catalyst-api.log" 2>&1 || true
    mesh_status "$m" > "$d/mesh.txt" 2>&1 || true
  done
  for f in "$DIR"/tofu-*.log "$DIR"/minter-build.log "$DIR"/mint.log "$DIR"/console-forward.log "$DIR"/discovery.txt "$DIR"/discovery-verdict.txt "$DIR"/mesh-*.txt \
           "$DIR"/api-image.txt "$DIR"/api-build.log "$DIR"/api/* \
           "$DIR"/kit-charts.txt "$DIR"/kit-plan.tsv "$DIR"/tree/*; do
    if [ -f "$f" ]; then cp "$f" "$DIR/evidence/" 2> /dev/null || true; fi
  done
  log "collect: evidence under $DIR/evidence"
}

# remove_volumes <name prefix>: docker volume rm of every volume of the pack, each within
# VOLUME_LIMIT seconds, logging how long each took. Exit 1 naming the first volume not removed.
remove_volumes() {
  local p=$1 v start rc all
  all=$(docker volume ls --format '{{.Name}}') || { echo "local-pack: docker cannot list the volumes" >&2; return 1; }
  while IFS= read -r v; do
    case $v in "$p"-*) ;; *) continue ;; esac
    start=$SECONDS
    rc=0
    timeout "$VOLUME_LIMIT" docker volume rm "$v" > /dev/null < /dev/null || rc=$?
    if [ "$rc" != 0 ]; then
      echo "local-pack: volume $v is not removed (docker volume rm exit $rc; 124 is the limit of ${VOLUME_LIMIT}s)" >&2
      return 1
    fi
    log "down: volume $v removed in $((SECONDS - start))s"
  done <<< "$all"
}

# pack_left <name prefix>: "<kind> <name>" for every container, volume and network of the pack
# still on this host. Exit 1 when docker cannot list them: then nothing is known.
pack_left() {
  local p=$1 kind all name
  for kind in container volume network; do
    if [ "$kind" = container ]; then all=$(docker container ls -a --format '{{.Names}}') || return 1
    else all=$(docker "$kind" ls --format '{{.Name}}') || return 1; fi
    while IFS= read -r name; do
      case $name in "$p" | "$p"-*) echo "$kind $name" ;; esac
    done <<< "$all"
  done
}

# down, in three parts. The provider (kreuzwerker/docker 3.9.0) gives the removal of a volume a
# fixed 30 seconds (plus the SDK's 30 seconds of grace) that no setting changes, and the volume
# rancher of a machine holds every image its k3s pulled: on the CI runner on 19 November the three
# took 22 s, 47 s and more than 50 s, so the destroy failed after a suite that passed. So the
# machines are destroyed first, then docker volume rm removes each volume of the pack with the
# time it needs (VOLUME_LIMIT), then the destroy of the rest finds the volumes gone. down passes
# only when no container, volume or network of the pack is left after that.
cmd_down() {
  local vars="$DIR/pack.tfvars.json" state="$DIR/terraform.tfstate" p left start=$SECONDS
  forward_stop
  [ -f "$state" ] || { log "down: nothing was started"; return 0; }
  p=$(pack_prefix "$vars") || die "down: the module inputs $vars cannot be read"
  tofu_run destroy -input=false -auto-approve -var-file="$vars" -state="$state" \
    -target=docker_container.control_plane -target=docker_container.worker > "$DIR/tofu-destroy-machines.log" 2>&1 \
    || { tail -40 "$DIR/tofu-destroy-machines.log"; die "tofu destroy of the machines failed"; }
  log "down: the machines are removed ($((SECONDS - start))s)"
  remove_volumes "$p" || die "down: a volume of the pack is not removed"
  tofu_run destroy -input=false -auto-approve -var-file="$vars" -state="$state" > "$DIR/tofu-destroy.log" 2>&1 \
    || { tail -40 "$DIR/tofu-destroy.log"; die "tofu destroy failed"; }
  left=$(pack_left "$p") || die "down: docker cannot list what is left of the pack"
  [ -z "$left" ] || { echo "$left" >&2; die "down: the lines above are left of the pack after the destroy"; }
  log "down: every machine, volume and network of the pack is removed ($((SECONDS - start))s)"
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

  # down, against a stand-in tofu and docker on PATH. The host is one file per kind under
  # $t/dn/host, one name per line, beside one container, volume and network of something else.
  # tofu destroy -target removes the machines of the pack; tofu destroy removes its network and,
  # like the provider with a volume that needs more than its 30 seconds, fails while a volume of
  # the pack is still there. docker volume rm hangs while $t/dn/host/hang exists; docker
  # container ls fails while $t/dn/host/nolist exists.
  local dn_p="catalyst-sov-example-test-01234567"
  mkdir -p "$t/dn/bin"
  cat > "$t/dn/bin/docker" << 'SH'
#!/usr/bin/env bash
h="$(dirname "$0")/../host"
[ ! -f "$h/nolist" ] || [ "$1 $2" != "container ls" ] || exit 1
case "$1 $2" in
  "container ls") cat "$h/container" ;;
  "volume ls") cat "$h/volume" ;;
  "network ls") cat "$h/network" ;;
  "volume rm")
    echo "volume rm $3" >> "$h/calls"
    [ ! -f "$h/hang" ] || exec sleep 60
    grep -qx -- "$3" "$h/volume" || exit 1
    grep -vx -- "$3" "$h/volume" > "$h/rest"
    mv "$h/rest" "$h/volume" ;;
  *) exit 3 ;;
esac
SH
  cat > "$t/dn/bin/tofu" << 'SH'
#!/usr/bin/env bash
h="$(dirname "$0")/../host"
p="catalyst-sov-example-test-01234567"
case " $* " in
  *" -target="*)
    echo "destroy machines" >> "$h/calls"
    grep -v "^$p-" "$h/container" > "$h/rest"
    mv "$h/rest" "$h/container" ;;
  *)
    echo "destroy" >> "$h/calls"
    if grep -q "^$p-" "$h/volume"; then echo "Error: timeout while waiting for state to become 'removed' (timeout: 30s)"; exit 1; fi
    [ -f "$h/keep-network" ] || { grep -vx "$p" "$h/network" > "$h/rest"; mv "$h/rest" "$h/network"; } ;;
esac
SH
  chmod +x "$t/dn/bin/docker" "$t/dn/bin/tofu"
  dn_case() { rm -rf "$t/dn/host" "$t/dn/dir"; mkdir -p "$t/dn/host" "$t/dn/dir"
    cp "$t/v2.json" "$t/dn/dir/pack.tfvars.json"; : > "$t/dn/dir/terraform.tfstate"; : > "$t/dn/host/calls"
    printf '%s\n' "$dn_p-region-a-cp1" "$dn_p-region-a-w1" "$dn_p-region-b-cp1" other > "$t/dn/host/container"
    printf '%s\n' "$dn_p-region-a-cp1-rancher" "$dn_p-region-a-cp1-kubelet" "$dn_p-region-b-cp1-rancher" other > "$t/dn/host/volume"
    printf '%s\n' bridge "$dn_p" > "$t/dn/host/network"
    for f in "$@"; do : > "$t/dn/host/$f"; done; }
  dn_run() { (PATH="$t/dn/bin:$PATH" DIR="$t/dn/dir" VOLUME_LIMIT=2 cmd_down) 2> "$t/dn/err"; }
  # dn_passed: down passed; the machines went first, then each volume of the pack, then the rest;
  # only what is not the pack's is left.
  dn_passed() { dn_run && [ "$(cat "$t/dn/host/calls")" = "$(printf '%s\n' "destroy machines" "volume rm $dn_p-region-a-cp1-rancher" \
      "volume rm $dn_p-region-a-cp1-kubelet" "volume rm $dn_p-region-b-cp1-rancher" destroy)" ] \
    && [ "$(cat "$t/dn/host/container" "$t/dn/host/volume" "$t/dn/host/network" | tr '\n' ' ')" = "other other bridge " ]; }
  dn_case
  expect 0 "down: a volume the provider cannot remove in its 30 s is removed by docker volume rm before the destroy, nothing of the pack is left" dn_passed
  dn_case hang
  expect 1 "down: a volume not removed within VOLUME_LIMIT fails down" dn_run
  expect 0 "down: ... naming the volume, and the destroy of the rest does not run" bash -c "grep -q 'volume $dn_p-region-a-cp1-rancher is not removed' '$t/dn/err' && ! grep -qx destroy '$t/dn/host/calls'"
  dn_case keep-network
  expect 1 "down: a network of the pack left after the destroy fails down" dn_run
  expect 0 "down: ... naming it" grep -qx "network $dn_p" "$t/dn/err"
  dn_case nolist
  expect 1 "down: when docker cannot list what is left, down fails" dn_run
  expect 0 "down: ... saying so" grep -q "docker cannot list what is left of the pack" "$t/dn/err"
  expect 1 "claim: no env file is a failure" env LOCAL_PACK_DIR="$t/none" bash "${BASH_SOURCE[0]}" claim
  expect 1 "discovery: no env file is a failure" env LOCAL_PACK_DIR="$t/none" bash "${BASH_SOURCE[0]}" discovery
  mkdir -p "$t/one"
  write_env "$t/one/env" http://127.0.0.1:18080 0123456789abcdef "$t/one/session" region-a
  expect 1 "discovery: one region is a failure" env LOCAL_PACK_DIR="$t/one" bash "${BASH_SOURCE[0]}" discovery

  # pin_flux, against a stand-in docker on PATH: the Flux objects are there; the Secret
  # flux-system/ghcr-pull is absent for the number of looks in $t/pin/host/absent, then there
  # (SEC-154: Flux renders it from the seed after first boot); a patch fails when
  # $t/pin/host/fail-<kind> exists. The calls and pin_flux's log go to one journal, in order.
  mkdir -p "$t/pin/bin"
  cat > "$t/pin/bin/docker" << 'SH'
#!/usr/bin/env bash
h="$(dirname "$0")/../host"
case "$*" in
  *" get kustomization bootstrap-kit") exit 0 ;;
  *" get secret ghcr-pull")
    n=$(( $(cat "$h/gets") + 1 )); echo "$n" > "$h/gets"
    if [ "$n" -gt "$(cat "$h/absent")" ]; then echo "get secret ghcr-pull: there" >> "$h/journal"; exit 0; fi
    echo "get secret ghcr-pull: absent" >> "$h/journal"; exit 1 ;;
  *" patch secret ghcr-pull "*) echo "patch secret ghcr-pull $(cat)" >> "$h/journal"; [ ! -f "$h/fail-secret" ] ;;
  *" patch gitrepository openova "*) echo "patch gitrepository openova $(cat)" >> "$h/journal"; [ ! -f "$h/fail-gitrepository" ] ;;
  *) exit 3 ;;
esac
SH
  chmod +x "$t/pin/bin/docker"
  local pin_c=0123456789abcdef0123456789abcdef01234567
  pin_case() { rm -rf "$t/pin/host"; mkdir -p "$t/pin/host"; echo 0 > "$t/pin/host/gets"; : > "$t/pin/host/journal"
    echo "$1" > "$t/pin/host/absent"; shift; for f in "$@"; do : > "$t/pin/host/$f"; done; }
  pin_run() { (PATH="$t/pin/bin:$PATH" GHCR_USER=u GHCR_TOKEN=p PULL_SECRET_LIMIT="${1:-600}" pin_flux region-a m1 wp/X "$pin_c") \
      >> "$t/pin/host/journal" 2> "$t/pin/err"; }
  # pin_at <text>: the number of the last journal line that holds the text, 0 for none.
  pin_at() { grep -n -F -- "$1" "$t/pin/host/journal" | tail -n 1 | cut -d: -f1 | grep . || echo 0; }
  # The Secret is absent twice: pin_flux waits, then patches it with the credential, after the
  # GitRepository, and logs once, after both patches.
  pin_late() { pin_case 2; pin_run || return 1
    local j="$t/pin/host/journal"
    [ "$(grep -c -x 'get secret ghcr-pull: absent' "$j")" = 2 ] && [ "$(grep -c -F 'patch secret ghcr-pull' "$j")" = 1 ] || return 1
    grep -q -x -F "patch gitrepository openova {\"spec\":{\"ref\":{\"branch\":\"wp/X\",\"commit\":\"$pin_c\"}}}" "$j" || return 1
    [ "$(pin_at 'patch secret ghcr-pull')" -gt "$(pin_at 'get secret ghcr-pull: there')" ] || return 1
    [ "$(grep -c -F 'pull credential set' "$j")" = 1 ] && grep -q -F "region-a: pull credential set, GitRepository at $pin_c" "$j" || return 1
    [ "$(pin_at 'pull credential set')" -gt "$(pin_at 'patch secret ghcr-pull')" ] \
      && [ "$(pin_at 'pull credential set')" -gt "$(pin_at 'patch gitrepository openova')" ] || return 1
    [ "$(sed -n 's/^patch secret ghcr-pull {"data":{".dockerconfigjson":"\([A-Za-z0-9+\/=]*\)"}}$/\1/p' "$j" | base64 -d)" \
      = '{"auths":{"ghcr.io":{"username":"u","password":"p","auth":"dTpw"}}}' ]; }
  # The Secret never appears: within PULL_SECRET_LIMIT the run stops naming the region and the
  # Secret; nothing is patched into it and nothing is logged as set.
  pin_never() { pin_case 1000000; ! pin_run 2 || return 1
    grep -q -F 'region-a: the Secret flux-system/ghcr-pull does not exist 2s after' "$t/pin/err" \
      && ! grep -q -e 'patch secret' -e 'pull credential set' "$t/pin/host/journal"; }
  # Either patch fails: the run stops naming the object and logs no "pull credential set".
  pin_failed() { pin_case 0 fail-secret; ! pin_run || return 1
    grep -q -F 'region-a: the patch of the Secret flux-system/ghcr-pull failed' "$t/pin/err" \
      && ! grep -q 'pull credential set' "$t/pin/host/journal" || return 1
    pin_case 0 fail-gitrepository; ! pin_run || return 1
    grep -q -F 'region-a: the patch of the GitRepository flux-system/openova failed' "$t/pin/err" \
      && ! grep -q -e 'patch secret' -e 'pull credential set' "$t/pin/host/journal"; }
  expect 0 "pin_flux: the pull Secret appears late and is patched" pin_late
  expect 0 "pin_flux: the pull Secret never appears and the failure names flux-system/ghcr-pull" pin_never
  expect 0 "pin_flux: a failed patch fails and prints no pull credential set" pin_failed

  # The kit charts of a run, on a throw-away tree: rel-a and rel-c are published charts of
  # folders of the tree; rel-b's chart depends on the library lib, which depends on lib2;
  # rel-gone's chart has no folder; rel-other is a chart of the name bp-a from another
  # repository, rel-up an upstream chart from a GitRepository.
  local kt="$t/kt" kk="$t/kt/$KIT_PATH"
  mkdir -p "$kt/platform/a/chart" "$kt/platform/b/chart" "$kt/platform/lib/chart" "$kt/platform/lib2/chart" "$kt/products/c/chart" "$kk"
  printf 'apiVersion: v2\nname: bp-a\nversion: 1.0.0\n' > "$kt/platform/a/chart/Chart.yaml"
  printf 'apiVersion: v2\nname: "bp-b"\nversion: 3.0.0\ndependencies:\n  - name: lib\n    version: 0.1.0\n    repository: "file://../../lib/chart"\n' > "$kt/platform/b/chart/Chart.yaml"
  printf 'apiVersion: v2\nname: lib\ntype: library\nversion: 0.1.0\ndependencies:\n  - name: lib2\n    version: 0.1.0\n    repository: file://../../lib2/chart\n' > "$kt/platform/lib/chart/Chart.yaml"
  printf 'apiVersion: v2\nname: lib2\ntype: library\nversion: 0.1.0\n' > "$kt/platform/lib2/chart/Chart.yaml"
  printf 'apiVersion: v2\nname: bp-c\nversion: 2.0.0\n' > "$kt/products/c/chart/Chart.yaml"
  printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - a.yaml\n  - b.yaml\n  - up.yaml\n' > "$kk/kustomization.yaml"
  kit_hr() { printf -- '---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: %s\n  namespace: flux-system\nspec:\n  interval: 15m\n  chart:\n    spec:\n      chart: %s\n%b      sourceRef:\n        kind: %s\n        name: %s\n' "$@"; }
  kit_src() { printf -- '---\napiVersion: source.toolkit.fluxcd.io/v1\nkind: %s\nmetadata:\n  name: %s\n  namespace: flux-system\nspec:\n  interval: 1h\n  url: %s\n' "$@"; }
  { kit_src HelmRepository repo oci://ghcr.io/openova-io
    kit_hr rel-a bp-a '      version: 1.0.0\n' HelmRepository repo
    kit_hr rel-c bp-c '      version: 2.0.0\n' HelmRepository repo
    kit_hr rel-gone bp-gone '      version: 1.0.0\n' HelmRepository repo; } > "$kk/a.yaml"
  kit_hr rel-b bp-b '      version: 3.0.0\n' HelmRepository repo > "$kk/b.yaml"
  { kit_src HelmRepository other https://charts.example.test
    kit_hr rel-other bp-a '      version: 9.9.9\n' HelmRepository other
    kit_src GitRepository upstream https://github.com/example/upstream
    kit_hr rel-up ./deploy/chart '' GitRepository upstream; } > "$kk/up.yaml"
  local tab=$'\t' fl="$t/kt-changed"
  kit_rel_want() { printf '%s\n' \
    "rel-a${tab}flux-system${tab}bp-a${tab}1.0.0${tab}HelmRepository${tab}repo${tab}oci://ghcr.io/openova-io" \
    "rel-b${tab}flux-system${tab}bp-b${tab}3.0.0${tab}HelmRepository${tab}repo${tab}oci://ghcr.io/openova-io" \
    "rel-c${tab}flux-system${tab}bp-c${tab}2.0.0${tab}HelmRepository${tab}repo${tab}oci://ghcr.io/openova-io" \
    "rel-gone${tab}flux-system${tab}bp-gone${tab}1.0.0${tab}HelmRepository${tab}repo${tab}oci://ghcr.io/openova-io" \
    "rel-other${tab}flux-system${tab}bp-a${tab}9.9.9${tab}HelmRepository${tab}other${tab}https://charts.example.test" \
    "rel-up${tab}flux-system${tab}./deploy/chart${tab}-${tab}GitRepository${tab}upstream${tab}https://github.com/example/upstream"; }
  kit_rel_case() { local got; got=$(kit_releases "$kk") || return 1; [ "$(printf '%s\n' "$got" | LC_ALL=C sort)" = "$(kit_rel_want)" ]; }
  expect 0 "kit_releases: each release of the kit with its chart, version and the URL of the source it names" kit_rel_case
  # plan_of <release>: the origin, folder and changed path of the release in "$t/kt-plan".
  plan_of() { awk -F'\t' -v r="$1" '$1 == r {print $8 "|" $9 "|" $10}' "$t/kt-plan"; }
  # plan_case <want for rel-a> <want for rel-b> <changed path>...: the plan of the throw-away tree
  # for those changed paths; rel-c is published, rel-gone has no folder, the other two are other
  # sources, whatever changed.
  plan_case() { local a=$1 b=$2; shift 2; printf '%s\n' "$@" > "$fl"; kit_plan "$kt" "$fl" > "$t/kt-plan" || return 1
    [ "$(plan_of rel-a)" = "$a" ] && [ "$(plan_of rel-b)" = "$b" ] && [ "$(plan_of rel-c)" = "published|products/c/chart|-" ] \
      && [ "$(plan_of rel-gone)" = "published|-|-" ] && [ "$(plan_of rel-other)" = "source|-|-" ] && [ "$(plan_of rel-up)" = "source|-|-" ] \
      && [ "$(wc -l < "$t/kt-plan")" = 6 ]; }
  expect 0 "kit_plan: a changed file of a chart's folder takes that chart from the tree, and only it" \
    plan_case "tree|platform/a/chart|platform/a/chart/values.yaml" "published|platform/b/chart|-" docs/x.md platform/a/chart/values.yaml
  expect 0 "kit_plan: a change of a file:// dependency of a dependency takes the chart that packages it from the tree" \
    plan_case "published|platform/a/chart|-" "tree|platform/b/chart|platform/lib2/chart/templates/_h.tpl" platform/lib2/chart/templates/_h.tpl
  expect 0 "kit_plan: a path beside a chart's folder, or of a folder whose name starts like it, takes nothing from the tree" \
    plan_case "published|platform/a/chart|-" "published|platform/b/chart|-" platform/a/README.md platform/a/chart-old/values.yaml platform/lib2/Chart.yaml ""
  plan_none() { kit_plan "$kt" > "$t/kt-plan" && [ "$(plan_of rel-a)" = "published|platform/a/chart|-" ] && [ "$(plan_of rel-b)" = "published|platform/b/chart|-" ]; }
  expect 0 "kit_plan: without a file of changed paths every chart is the published one" plan_none
  expect 1 "kit_plan: a file of changed paths that does not exist is refused" kit_plan "$kt" "$t/no-such-file"
  mkdir -p "$t/kt2/platform/a/chart" "$t/kt2/products/a/chart"
  cp "$kt/platform/a/chart/Chart.yaml" "$t/kt2/platform/a/chart/Chart.yaml"
  cp "$kt/platform/a/chart/Chart.yaml" "$t/kt2/products/a/chart/Chart.yaml"
  expect 1 "chart_folders: two folders that hold a chart of one name are refused" chart_folders "$t/kt2"
  plan_case "tree|platform/a/chart|platform/a/chart/values.yaml" "published|platform/b/chart|-" platform/a/chart/values.yaml || true
  awk -F'\t' -v OFS='\t' '$1 == "rel-a" {$9 = "platform/a b/chart"} {print}' "$t/kt-plan" > "$t/kt-plan-bad"
  # patch_of <plan file>: tree_patch of that plan.
  patch_of() { tree_patch < "$1"; }
  expect 1 "tree_patch: a folder with a character the flow mapping does not quote is refused" patch_of "$t/kt-plan-bad"
  kit_plan "$kt" > "$t/kt-plan-none"
  expect 1 "tree_patch: a plan with no chart from the tree has no patch" patch_of "$t/kt-plan-none"

  # The real kit and tree. The change of CMP-065 (pull request 8862) was in the chart folder of
  # bp-kyverno-policies; its run installed the published chart and the defect reached main.
  printf '%s\n' platform/kyverno-policies/chart/templates/baseline/24-forbid-nodeport-service.yaml docs/ledger/plan.yaml > "$t/cmp065"
  real_tree() { kit_plan "$ROOT" "$t/cmp065" > "$t/real-plan" || return 1
    [ "$(awk -F'\t' '$8 == "tree" {print $1 "|" $3 "|" $9 "|" $10}' "$t/real-plan")" \
      = "bp-kyverno-policies|bp-kyverno-policies|platform/kyverno-policies/chart|platform/kyverno-policies/chart/templates/baseline/24-forbid-nodeport-service.yaml" ] \
      && [ "$(awk -F'\t' '$1 == "local-path" {print $8}' "$t/real-plan")" = source ] \
      && [ "$(awk -F'\t' '$8 != "tree" && $1 != "local-path" && $8 != "published"' "$t/real-plan")" = "" ]; }
  expect 0 "kit_plan, the real kit: the change of CMP-065 takes bp-kyverno-policies, and only it, from the tree" real_tree
  printf '%s\n' platform/topology-lib/chart/templates/_placement.tpl > "$t/toplib"
  real_lib() { kit_plan "$ROOT" "$t/toplib" > "$t/real-plan-lib" || return 1
    [ "$(awk -F'\t' '$8 == "tree" {print $1 "|" $9}' "$t/real-plan-lib" | tr '\n' ' ')" = "bp-postgres-shared|platform/postgres/chart bp-postgres-shared-c|platform/postgres/chart " ]; }
  expect 0 "kit_plan, the real kit: a change of the library topology-lib takes both releases of bp-postgres from the tree" real_lib
  # The patch, the way Flux applies it: sovereign-root's new entry patches the Kustomization
  # bootstrap-kit of the root folder; bootstrap-kit's patches then patch the kit. Both renders are
  # kubectl kustomize, the kustomize of kustomize-controller.
  mkdir -p "$t/root" "$t/kitp"
  real_chain() {
    tree_patch < "$t/real-plan" > "$t/patch.json" || return 1
    [ "$(yq -p json -o tsv '[[length, .[0].op, .[0].path] | join("|")]' "$t/patch.json")" = "1|add|/spec/patches/-" ] || return 1
    R=$(realpath --relative-to="$t/root" "$ROOT/clusters/_template/root/base-primary") M=$(yq -p json -o tsv '[.[0].value.patch]' "$t/patch.json") \
      yq -n '.apiVersion = "kustomize.config.k8s.io/v1beta1" | .kind = "Kustomization" | .resources = [strenv(R)] | .patches = [{"patch": strenv(M)}]' > "$t/root/kustomization.yaml"
    kubectl kustomize --load-restrictor LoadRestrictionsNone "$t/root" > "$t/root.out" || return 1
    kubectl kustomize --load-restrictor LoadRestrictionsNone "$ROOT/clusters/_template/root/base-primary" > "$t/root-plain.out" || return 1
    local sel='select(.kind == "Kustomization" and .metadata.name == "bootstrap-kit")'
    # ysel <expression> <file>: yq of every document, without the empty line of each document
    # the selection leaves out.
    ysel() { yq -N "$1" "$2" | sed '/^$/d'; }
    # bootstrap-kit gains its patches and keeps everything else.
    [ "$(ysel "$sel | del(.spec.patches)" "$t/root.out")" = "$(ysel "$sel" "$t/root-plain.out")" ] || return 1
    [ "$(ysel "$sel | .spec.patches | length" "$t/root.out")" = 1 ] || return 1
    R=$(realpath --relative-to="$t/kitp" "$ROOT/$KIT_PATH") yq -n '.apiVersion = "kustomize.config.k8s.io/v1beta1" | .kind = "Kustomization" | .resources = [strenv(R)]' > "$t/kitp/kustomization.yaml"
    { echo "patches:"; ysel "$sel | .spec.patches" "$t/root.out"; } >> "$t/kitp/kustomization.yaml"
    kit_releases "$t/kitp" > "$t/kitp.rel" && kit_releases "$ROOT/$KIT_PATH" > "$t/kit.rel" || return 1
    # bp-kyverno-policies takes its chart from the tree; every other release is as before.
    [ "$(grep "^bp-kyverno-policies$tab" "$t/kitp.rel")" = "bp-kyverno-policies${tab}flux-system${tab}./platform/kyverno-policies/chart${tab}*${tab}GitRepository${tab}openova${tab}-" ] \
      && [ "$(grep -v "^bp-kyverno-policies$tab" "$t/kitp.rel")" = "$(grep -v "^bp-kyverno-policies$tab" "$t/kit.rel")" ] \
      && [ "$(grep -c . "$t/kit.rel")" = "$(grep -c . "$t/kitp.rel")" ]; }
  expect 0 "tree_patch, rendered as Flux applies it: bootstrap-kit gives bp-kyverno-policies its chart from the tree of the commit, every other release keeps its own" real_chain

  # tree_judge on a HelmRelease and its HelmChart. The artifact of the tree was built at
  # 03:10:00 from ./platform/x/chart at the commit c.
  local c=0123456789abcdef0123456789abcdef01234567 tj="$t/tj"
  mkdir -p "$tj"
  # tj_hc <commit> <source kind>: the HelmChart.
  tj_hc() { printf '{"spec":{"sourceRef":{"kind":"%s","name":"openova"},"chart":"./platform/x/chart"},"status":{"observedSourceArtifactRevision":"wp/X@sha1:%s","artifact":{"revision":"1.0.72","lastUpdateTime":"2026-11-27T03:10:00Z"},"conditions":[{"type":"Ready","status":"True"}]}}\n' "$2" "$1" > "$tj/hc.json"; }
  # tj_hr <source kind> <generation> <observed> <Ready> <history snapshot JSON or ""> [suspend]
  tj_hr() { printf '{"metadata":{"name":"bp-x","generation":%s},"spec":{"suspend":%s,"chart":{"spec":{"chart":"%s","sourceRef":{"kind":"%s","name":"%s"}}}},"status":{"observedGeneration":%s,"helmChart":"flux-system/flux-system-bp-x","conditions":[{"type":"Released","status":"True"},{"type":"Ready","status":"%s","reason":"R"}],"history":[%s]}}\n' \
      "$2" "${6:-false}" "$([ "$1" = GitRepository ] && echo ./platform/x/chart || echo bp-x)" "$1" "$([ "$1" = GitRepository ] && echo openova || echo bp-x)" "$3" "$4" "$5" > "$tj/hr.json"; }
  snap() { printf '{"chartName":"%s","chartVersion":"%s","status":"%s","lastDeployed":"%s"}' "$@"; }
  local fresh stale_at same_at
  fresh=$(snap bp-x 1.0.72 deployed 2026-11-27T03:10:07Z) stale_at=$(snap bp-x 1.0.72 deployed 2026-11-27T03:09:00Z) same_at=$(snap bp-x 1.0.72 deployed 2026-11-27T03:10:00Z)
  tj() { tree_judge "$tj/hr.json" "$tj/hc.json" bp-x platform/x/chart "$c" "$1"; }
  # tj_case <exit code> <start of the line> <required>: tree_judge exits so and prints that.
  tj_case() { local out st=0; out=$(tj "$3") || st=$?; [ "$st" = "$1" ] && [ "${out#"$2"}" != "$out" ]; }
  tj_hc "$c" GitRepository
  tj_hr GitRepository 2 2 True "$fresh"
  expect 0 "tree_judge: the newest release is the chart of the tree, deployed after its artifact, Ready: installed" tj_case 0 "installed from the tree: bp-x 1.0.72 of ./platform/x/chart at $c" yes
  tj_hr HelmRepository 2 2 True "$fresh"
  expect 0 "tree_judge: a release that still names its HelmRepository waits" tj_case 1 "waiting: the HelmRelease takes its chart from HelmRepository/bp-x" no
  tj_hr GitRepository 2 2 True "$fresh"
  tj_hc fedcba9876543210fedcba9876543210fedcba98 GitRepository
  expect 0 "tree_judge: a HelmChart built from another commit waits" tj_case 1 "waiting: its HelmChart is not built" yes
  echo '{}' > "$tj/hc.json"
  expect 0 "tree_judge: no HelmChart waits" tj_case 1 "waiting: its HelmChart is not built" no
  tj_hc "$c" GitRepository
  tj_hr GitRepository 2 2 True "$stale_at"
  expect 0 "tree_judge: a release deployed before the artifact of the tree, handled and Ready: stale" tj_case 3 "stale:" no
  tj_hr GitRepository 2 2 True "$same_at"
  expect 0 "tree_judge: a release deployed in the same second as the artifact counts as before it: stale" tj_case 3 "stale:" yes
  tj_hr GitRepository 2 2 True "$(snap bp-x 1.0.71 deployed 2026-11-27T03:12:00Z)"
  expect 0 "tree_judge: a newest release of another version than the artifact of the tree: stale" tj_case 3 "stale:" yes
  tj_hr GitRepository 2 1 True "$stale_at"
  expect 0 "tree_judge: a release deployed before the artifact whose new generation is not handled yet waits" tj_case 1 "waiting: the newest release" yes
  tj_hr GitRepository 2 2 Unknown "$stale_at"
  expect 0 "tree_judge: a release deployed before the artifact that is progressing waits" tj_case 1 "waiting: the newest release" yes
  tj_hr GitRepository 2 2 False ""
  expect 0 "tree_judge: not installed, required, waits" tj_case 1 "waiting: not installed yet" yes
  expect 0 "tree_judge: not installed, not required, is done and says so" tj_case 0 "names the tree, not installed" no
  tj_hr GitRepository 2 2 False "$(snap bp-x 1.0.72 failed 2026-11-27T03:10:30Z)"
  expect 0 "tree_judge: a failed release of the tree, required, waits" tj_case 1 "waiting: bp-x 1.0.72 of the tree is failed" yes
  expect 0 "tree_judge: a failed release of the tree, not required, is done and says so" tj_case 0 "installed from the tree: bp-x 1.0.72 of ./platform/x/chart at $c, failed, Ready=False" no
  tj_hr GitRepository 3 2 True "$fresh"
  expect 0 "tree_judge: Ready at an older generation, required, waits" tj_case 1 "waiting: bp-x 1.0.72 of the tree is deployed" yes
  tj_hr HelmRepository 2 2 True "$stale_at" true
  expect 0 "tree_judge: a suspended release is not judged" tj_case 0 "suspended, not judged" yes

  # tree_states and tree_redirect against a stand-in docker: the HelmRelease and HelmChart of
  # bp-x come from $t/ts/host; every annotate and patch goes to the journal with its body. As on
  # k3s, the short name helmchart is the kind of helm.cattle.io, which does not hold the HelmChart.
  mkdir -p "$t/ts/bin" "$t/ts/host" "$t/ts/dir/tree"
  cat > "$t/ts/bin/docker" << 'SH'
#!/usr/bin/env bash
h="$(dirname "$0")/../host"
case "$*" in
  *" get helmrelease bp-x -o json") cat "$h/hr.json" ;;
  *" get helmcharts.source.toolkit.fluxcd.io flux-system-bp-x -o json") [ -f "$h/hc.json" ] && cat "$h/hc.json" \
    || { echo 'Error from server (NotFound): helmcharts.source.toolkit.fluxcd.io "flux-system-bp-x" not found' >&2; exit 1; } ;;
  *" get helmchart flux-system-bp-x -o json") echo 'Error from server (NotFound): helmcharts.helm.cattle.io "flux-system-bp-x" not found' >&2; exit 1 ;;
  *" annotate helmrelease bp-x --overwrite "*) shift 13; echo "annotate $*" >> "$h/journal" ;;
  *" get kustomization sovereign-root") [ -f "$h/root" ] ;;
  *" get kustomization bootstrap-kit") [ -f "$h/kit" ] ;;
  *" patch kustomization sovereign-root --type json --patch-file /dev/stdin") echo "patch $(cat)" >> "$h/journal"; [ ! -f "$h/fail-patch" ] ;;
  *) exit 3 ;;
esac
SH
  chmod +x "$t/ts/bin/docker"
  printf 'bp-x\tflux-system\tbp-x\t1.0.72\tHelmRepository\tbp-x\toci://ghcr.io/openova-io\ttree\tplatform/x/chart\tplatform/x/chart/values.yaml\n' > "$t/ts/dir/tree-charts.tsv"
  cp "$tj/hc.json" "$t/ts/host/hc.json"
  ts_run() { (PATH="$t/ts/bin:$PATH" DIR="$t/ts/dir" tree_states region-a m1 "bp-x" "$c") > "$t/ts/out" 2> "$t/ts/err"; }
  ts_stale() { : > "$t/ts/host/journal"; rm -f "$t/ts/dir/tree/"*; tj_hr GitRepository 2 2 True "$stale_at"; cp "$tj/hr.json" "$t/ts/host/hr.json"
    ! ts_run || return 1
    grep -q '^bp-x: stale:' "$t/ts/out" && grep -q '^bp-x: stale:' "$t/ts/dir/tree/region-a.txt" || return 1
    ! ts_run || return 1
    [ "$(grep -c '^annotate ' "$t/ts/host/journal")" = 1 ] \
      && grep -q -x 'annotate reconcile.fluxcd.io/requestedAt=\(local-pack-[0-9]*\) reconcile.fluxcd.io/forceAt=\1' "$t/ts/host/journal"; }
  ts_done() { : > "$t/ts/host/journal"; rm -f "$t/ts/dir/tree/"*; tj_hr GitRepository 2 2 True "$fresh"; cp "$tj/hr.json" "$t/ts/host/hr.json"
    ts_run && [ ! -s "$t/ts/out" ] && [ ! -s "$t/ts/host/journal" ] && grep -q '^bp-x: installed from the tree: bp-x 1.0.72' "$t/ts/dir/tree/region-a.txt" \
      && cmp -s "$t/ts/host/hc.json" "$t/ts/dir/tree/region-a-bp-x-helmchart.json"; }
  ts_unread() { local st=0; : > "$t/ts/host/journal"; rm -f "$t/ts/dir/tree/"*; tj_hr GitRepository 2 2 True "$fresh"; cp "$tj/hr.json" "$t/ts/host/hr.json"
    mv "$t/ts/host/hc.json" "$t/ts/host/hc.json.aside"; ts_run || st=$?; mv "$t/ts/host/hc.json.aside" "$t/ts/host/hc.json"; [ "$st" = 1 ] \
      && grep -q '^bp-x: waiting: its HelmChart is not built' "$t/ts/out" && [ "$(cat "$t/ts/dir/tree/region-a-bp-x-helmchart.json")" = '{}' ] \
      && [ "$(cat "$t/ts/dir/tree/region-a-bp-x-helmchart.err")" = 'Error from server (NotFound): helmcharts.source.toolkit.fluxcd.io "flux-system-bp-x" not found' ]; }
  ts_absent() { : > "$t/ts/host/journal"; rm -f "$t/ts/dir/tree/"* "$t/ts/host/hr.json"
    ! ts_run && grep -q '^bp-x: waiting: the HelmRelease takes its chart from ?/?' "$t/ts/out"; }
  expect 0 "tree_states: a stale release gets exactly one forced upgrade (requestedAt and forceAt one token) and is not done" ts_stale
  expect 0 "tree_states: a release installed from the tree is done and is not touched; its HelmChart is read by the full name of the Flux kind (on k3s helmchart is helm.cattle.io's)" ts_done
  expect 0 "tree_states: a HelmChart that cannot be read waits, and its error is kept beside it" ts_unread
  expect 0 "tree_states: a release that cannot be read waits" ts_absent
  rr_run() { (PATH="$t/ts/bin:$PATH" DIR="$t/ts/dir" tree_redirect region-a m1) > "$t/ts/out" 2> "$t/ts/err"; }
  rr_cases() { : > "$t/ts/host/journal"; rm -f "$t/ts/host/root" "$t/ts/host/kit" "$t/ts/host/fail-patch"
    ! rr_run && [ ! -s "$t/ts/host/journal" ] || return 1
    : > "$t/ts/host/root"
    rr_run && [ "$(cat "$t/ts/host/journal")" = "patch $(tree_patch < "$t/ts/dir/tree-charts.tsv")" ] \
      && grep -q 'region-a: bootstrap-kit takes bp-x from the tree (sovereign-root patched; bootstrap-kit existed before: no)' "$t/ts/out" || return 1
    : > "$t/ts/host/fail-patch"
    ! rr_run && grep -q -F 'region-a: the patch of the Kustomization flux-system/sovereign-root failed' "$t/ts/err"; }
  expect 0 "tree_redirect: waits for sovereign-root, then appends the entry of tree_patch to it; a failed patch stops the run" rr_cases

  # The catalyst-api image of a run. The real Containerfile names the paths it is built from.
  api_real() { [ "$(api_sources "$ROOT")" = "$(printf '%s\n' core/controllers core/services/shared infra/providers \
      products/catalyst/bootstrap/api "$API_CONTAINERFILE" products/catalyst/bootstrap/api/go.mod products/catalyst/bootstrap/api/go.sum)" ]; }
  expect 0 "api_sources, the real Containerfile: itself, core/controllers, core/services/shared, the API's folder (its go.mod and go.sum) and infra/providers" api_real
  local at="$t/api" af="$t/api/$API_CONTAINERFILE" bad
  mkdir -p "${af%/*}"
  # af_case <line>...: the Containerfile of the throw-away tree $t/api holds these lines.
  af_case() { printf '%s\n' 'FROM scratch AS build' "$@" > "$af"; }
  af_case 'COPY --from=build /out/x /usr/local/bin/x' 'copy --chown=65534:65534 ./lib/a/ b.txt /dst/' '# COPY not/this /x' '  COPY --link d /d'
  expect 0 "api_sources: each source of a COPY from the build context, after its flags, and no COPY --from another stage" \
    test "$(api_sources "$at" | tr '\n' ' ')" = "b.txt d lib/a $API_CONTAINERFILE "
  for bad in 'COPY ["a", "/b"]' 'COPY a/*.go /x/' 'COPY ../x /x' 'COPY a/../../x /x' 'COPY . /app' 'COPY /etc/x /x' \
             'ADD https://example.test/x /x' "COPY a \\" 'COPY a' 'COPY <<EOF /x'; do
    af_case 'COPY d /d' "$bad"
    expect 1 "api_sources: a line it does not read is refused: $bad" api_sources "$at"
  done
  af_case 'COPY --from=build /out/x /x' 'RUN true'
  expect 1 "api_sources: a Containerfile that copies nothing from the build context is refused" api_sources "$at"
  rm -f "$af"
  expect 1 "api_sources: no Containerfile is refused" api_sources "$at"
  # ap_case <want> <changed path>...: api_plan of the real tree for those changed paths.
  ap_case() { local want=$1; shift; printf '%s\n' "$@" > "$t/ap-changed"; [ "$(api_plan "$ROOT" "$t/ap-changed")" = "$want" ]; }
  expect 0 "api_plan: a changed file of the API's folder takes the image from the tree" \
    ap_case "tree${tab}products/catalyst/bootstrap/api/internal/handler/version.go" docs/ledger/plan.yaml products/catalyst/bootstrap/api/internal/handler/version.go
  expect 0 "api_plan: ... naming the first such path of the file (here a library the image copies)" \
    ap_case "tree${tab}core/services/shared/auth/token.go" core/services/shared/auth/token.go products/catalyst/bootstrap/api/go.mod
  expect 0 "api_plan: a changed file of infra/providers, which the image bundles, takes it from the tree" \
    ap_case "tree${tab}infra/providers/local/main.tf" infra/providers/local/main.tf
  expect 0 "api_plan: the probe path of the workflow, the Containerfile, takes it from the tree" ap_case "tree${tab}$API_CONTAINERFILE" "$API_CONTAINERFILE"
  expect 0 "api_plan: a path beside those paths, or of a folder whose name starts like one, keeps the pinned image" \
    ap_case "pinned${tab}-" products/catalyst/bootstrap/ui/src/x.ts products/catalyst/bootstrap/api-old/x.go core/controllers-old/x.go \
      infra/providers.md products/catalyst/chart/values.yaml ""
  expect 0 "api_plan: without a file of changed paths the image is the pinned one" test "$(api_plan "$ROOT")" = "pinned${tab}-"
  expect 1 "api_plan: a file of changed paths that does not exist is refused" api_plan "$ROOT" "$t/no-such-file"
  printf '%s\n' docs/ledger/plan.yaml products/catalyst/bootstrap/api/internal/handler/version.go > "$t/charts-api"
  expect 0 "charts: the last line says that the console runs the catalyst-api image of the tree, and why" \
    test "$(bash "${BASH_SOURCE[0]}" charts --changed "$t/charts-api" | tail -n 1)" \
    = "catalyst-api: TREE, the image built with $API_CONTAINERFILE from the tree of this change (changed: products/catalyst/bootstrap/api/internal/handler/version.go)"
  printf '%s\n' docs/ledger/plan.yaml > "$t/charts-docs"
  expect 0 "charts: ... and otherwise that it runs the pinned image" test "$(bash "${BASH_SOURCE[0]}" charts --changed "$t/charts-docs" | tail -n 1)" \
    = "catalyst-api: pinned, the image the chart of $CONSOLE_RELEASE names (no changed path is a path the image is built from)"

  # tree_patch with the tag of the image of the tree, rendered as Flux applies it (real_chain).
  local atag=tree-0123456789ab
  tp_none() { tree_patch "$@" < /dev/null; }
  expect 1 "tree_patch: a tag up does not make is refused" tp_none tree-0123456789AB flux-system
  expect 1 "tree_patch: a namespace that is not a name is refused" tp_none "$atag" 'flux system'
  # tp_alone: with no chart from the tree, the one entry patches bootstrap-kit with one patch, the
  # HelmRelease of the console release in flux-system with the tag and nothing else.
  tp_alone() { tree_patch "$atag" flux-system < "$t/kt-plan-none" > "$t/apatch1.json" || return 1
    [ "$(yq -p json -o tsv '[length]' "$t/apatch1.json")" = 1 ] || return 1
    yq -p json -o tsv '[.[0].value.patch]' "$t/apatch1.json" > "$t/apatch1.yaml"
    [ "$(yq -o tsv '[[.kind, .metadata.name, .metadata.namespace, (.spec.patches | length)] | join("|")]' "$t/apatch1.yaml")" = "Kustomization|bootstrap-kit|flux-system|1" ] || return 1
    yq '.spec.patches[0].patch' "$t/apatch1.yaml" > "$t/apatch1-hr.yaml"
    [ "$(yq -o tsv '[[.kind, .metadata.name, .metadata.namespace, .spec.values.images.catalystApi.tag, (.spec | keys | join(",")), (.spec.values | keys | join(","))] | join("|")]' "$t/apatch1-hr.yaml")" \
      = "HelmRelease|bp-catalyst-platform|flux-system|$atag|values|images" ]; }
  expect 0 "tree_patch: a tag alone, with no chart from the tree, is one patch: the console release's images.catalystApi.tag" tp_alone
  # ajson <selection> <file>: the selected objects of a render as JSON, keys sorted.
  ajson() { yq -N -o json -I0 "$1 | sort_keys(..)" "$2"; }
  api_chain() {
    tree_patch "$atag" flux-system < "$t/real-plan" > "$t/apatch.json" || return 1
    R=$(realpath --relative-to="$t/root" "$ROOT/clusters/_template/root/base-primary") M=$(yq -p json -o tsv '[.[0].value.patch]' "$t/apatch.json") \
      yq -n '.apiVersion = "kustomize.config.k8s.io/v1beta1" | .kind = "Kustomization" | .resources = [strenv(R)] | .patches = [{"patch": strenv(M)}]' > "$t/root/kustomization.yaml"
    kubectl kustomize --load-restrictor LoadRestrictionsNone "$t/root" > "$t/aroot.out" || return 1
    R=$(realpath --relative-to="$t/kitp" "$ROOT/$KIT_PATH") yq -n '.apiVersion = "kustomize.config.k8s.io/v1beta1" | .kind = "Kustomization" | .resources = [strenv(R)]' > "$t/kitp/kustomization.yaml"
    { echo "patches:"; yq -N 'select(.kind == "Kustomization" and .metadata.name == "bootstrap-kit") | .spec.patches' "$t/aroot.out" | sed '/^$/d'; } >> "$t/kitp/kustomization.yaml"
    kubectl kustomize --load-restrictor LoadRestrictionsNone "$t/kitp" > "$t/akit.out" || return 1
    kubectl kustomize --load-restrictor LoadRestrictionsNone "$ROOT/$KIT_PATH" > "$t/kit-plain.out" || return 1
    local con='select(.kind == "HelmRelease" and .metadata.name == "bp-catalyst-platform")'
    local rest='select((.kind == "HelmRelease" and (.metadata.name == "bp-catalyst-platform" or .metadata.name == "bp-kyverno-policies")) | not)'
    # The console release gains the tag and keeps every other value; bp-kyverno-policies takes its
    # chart from the tree as before; every other object of the kit is as it was.
    [ "$(yq -N "$con | .spec.values.images.catalystApi.tag" "$t/akit.out" | sed '/^$/d')" = "$atag" ] \
      && [ "$(yq -N "$con | .spec.values.images.catalystApi.tag" "$t/kit-plain.out" | sed '/^$/d')" = null ] \
      && [ "$(ajson "$con | del(.spec.values.images)" "$t/akit.out" | sed '/^$/d')" = "$(ajson "$con" "$t/kit-plain.out" | sed '/^$/d')" ] \
      && [ "$(yq -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-kyverno-policies") | .spec.chart.spec.chart' "$t/akit.out" | sed '/^$/d')" = ./platform/kyverno-policies/chart ] \
      && [ "$(ajson "$rest" "$t/akit.out" | sed '/^$/d')" = "$(ajson "$rest" "$t/kit-plain.out" | sed '/^$/d')" ] \
      && [ "$(grep -c '^kind: ' "$t/akit.out")" = "$(grep -c '^kind: ' "$t/kit-plain.out")" ]; }
  expect 0 "tree_patch with a tag, rendered as Flux applies it: the console release gets images.catalystApi.tag and keeps every other value; the chart of the tree and every other object are as before" api_chain

  expect 0 "machines: per region its control-plane machine, then its workers" test "$(machines "$t/v2.json" | tr '\n' ' ')" \
    = "catalyst-sov-example-test-01234567-region-a-cp1 catalyst-sov-example-test-01234567-region-a-w1 catalyst-sov-example-test-01234567-region-b-cp1 "
  expect 1 "machines: inputs that cannot be read are refused" machines /dev/null

  # api_judge on a Deployment and its pods.
  local aref="$API_REPO:$atag" arepo="$API_REPO" pref="$API_REPO:3f899a9" adig pdig aj="$t/aj"
  adig="sha256:$(printf 'a%.0s' $(seq 64))" pdig="sha256:$(printf 'b%.0s' $(seq 64))"
  mkdir -p "$aj"
  # aj_dep <generation> <observed> <replicas> <updated> <available> <image>: the Deployment.
  aj_dep() { printf '{"metadata":{"name":"catalyst-api","generation":%s},"spec":{"replicas":%s,"template":{"spec":{"containers":[{"name":"catalyst-api","image":"%s"}]}}},"status":{"observedGeneration":%s,"replicas":%s,"updatedReplicas":%s,"availableReplicas":%s}}\n' \
      "$1" "$3" "$6" "$2" "$3" "$4" "$5" > "$aj/dep.json"; }
  # aj_pod <name> <owner> <ready> <imageID> [deleting]: one pod (a ReplicaSet's when the owner is
  # catalyst-api-<hash>, else a Job's) with a sidecar beside catalyst-api.
  aj_pod() { local del="" kind=Job
    [ -z "${5:-}" ] || del='"deletionTimestamp":"2026-11-27T10:00:00Z",'
    [ "${2#catalyst-api-}" = "$2" ] || kind=ReplicaSet
    printf '{"metadata":{"name":"%s",%s"ownerReferences":[{"kind":"%s","name":"%s"}]},"status":{"containerStatuses":[{"name":"sidecar","ready":true,"imageID":"x@sha256:c"},{"name":"catalyst-api","ready":%s,"imageID":"%s"}]}}' \
      "$1" "$del" "$kind" "$2" "$3" "$4"; }
  aj_pods() { local IFS=,; printf '{"items":[%s]}\n' "$*" > "$aj/pods.json"; }
  # aj_case <exit code> <start of the line> <ref> <digest> <required>: api_judge exits so and prints that.
  aj_case() { local out st=0; out=$(api_judge "$aj/dep.json" "$aj/pods.json" "$3" "$4" "$5") || st=$?; [ "$st" = "$1" ] && [ "${out#"$2"}" != "$out" ]; }
  aj_dep 2 2 1 1 1 "$aref"
  aj_pods "$(aj_pod catalyst-api-abc-x1 catalyst-api-abc true "$arepo@$adig")"
  expect 0 "api_judge: every pod of the Deployment runs the digest of the image of the tree: runs, naming the image, the digest and the pods" \
    aj_case 0 "runs $aref of the tree as $arepo@$adig: 1 pod(s) (catalyst-api-abc-x1), Ready" "$aref" "$adig" yes
  aj_pods "$(aj_pod catalyst-api-abc-x1 catalyst-api-abc true "$arepo@$pdig")"
  expect 0 "api_judge: a pod that runs another digest waits, naming both" \
    aj_case 1 "waiting: the pod catalyst-api-abc-x1 runs $arepo@$pdig (Ready=true), not $arepo@$adig" "$aref" "$adig" yes
  aj_pods "$(aj_pod catalyst-api-abc-x1 catalyst-api-abc false "$arepo@$adig")"
  expect 0 "api_judge: a pod that is not Ready waits" aj_case 1 "waiting: the pod catalyst-api-abc-x1 runs $arepo@$adig (Ready=false)" "$aref" "$adig" yes
  aj_pods "$(aj_pod catalyst-api-abc-x1 catalyst-api-abc true "$arepo@$adig")" "$(aj_pod catalyst-api-old-x2 catalyst-api-old true "$arepo@$pdig")"
  expect 0 "api_judge: a pod of another ReplicaSet that still runs the pinned image waits" \
    aj_case 1 "waiting: the pod catalyst-api-old-x2 runs $arepo@$pdig (Ready=true), not $arepo@$adig" "$aref" "$adig" yes
  aj_pods "$(aj_pod catalyst-api-abc-x1 catalyst-api-abc true "$arepo@$adig")" "$(aj_pod catalyst-api-old-x2 catalyst-api-old true "$arepo@$pdig" deleting)" \
    "$(aj_pod check-x3 check true "$arepo@$pdig")"
  expect 0 "api_judge: a pod being deleted and the pod of a Job are not pods of the Deployment" aj_case 0 "runs $aref of the tree" "$aref" "$adig" yes
  aj_pods
  expect 0 "api_judge: a Deployment without a pod waits" aj_case 1 "waiting: the Deployment has 0 pod(s) of 1 replica(s): none" "$aref" "$adig" yes
  aj_pods "$(aj_pod catalyst-api-abc-x1 catalyst-api-abc true "$arepo@$adig")"
  aj_dep 2 2 1 1 1 "$pref"
  expect 0 "api_judge: a Deployment that names the pinned image waits" aj_case 1 "waiting: the Deployment names $pref, not $aref" "$aref" "$adig" yes
  expect 0 "api_judge: ... in a secondary region too, when it has one" aj_case 1 "waiting: the Deployment names $pref, not $aref" "$aref" "$adig" no
  aj_dep 3 2 1 1 1 "$aref"
  expect 0 "api_judge: a generation the Deployment has not handled waits" aj_case 1 "waiting: the Deployment of $aref has handled generation 2 of 3" "$aref" "$adig" yes
  aj_dep 2 2 1 1 0 "$aref"
  expect 0 "api_judge: a replica that is not available waits" \
    aj_case 1 "waiting: the Deployment of $aref has handled generation 2 of 2; 1 updated and 0 available of 1" "$aref" "$adig" yes
  echo '{}' > "$aj/dep.json"
  expect 0 "api_judge: no Deployment in the primary region waits" aj_case 1 "waiting: no Deployment catalyst-system/catalyst-api" "$aref" "$adig" yes
  expect 0 "api_judge: no Deployment in a secondary region is said and not judged" aj_case 0 "no Deployment catalyst-system/catalyst-api in this region" "$aref" "$adig" no
  aj_dep 2 2 1 1 1 "$pref"
  aj_pods "$(aj_pod catalyst-api-def-y1 catalyst-api-def true "$arepo@$pdig")"
  expect 0 "api_judge: the pinned image is named with the imageID its pod runs" \
    aj_case 0 "runs the pinned image $pref as $arepo@$pdig: 1 pod(s) (catalyst-api-def-y1), Ready" - - yes
  aj_pods "$(aj_pod catalyst-api-def-y1 catalyst-api-def true "")"
  expect 0 "api_judge: the pinned image, a pod that reports no imageID yet waits" aj_case 1 "waiting: the pod catalyst-api-def-y1 runs no image yet (Ready=true)" - - yes

  # api_build against a stand-in docker: image inspect answers whether $t/ab/host/present exists;
  # each build logs its arguments and takes the next word of $t/ab/host/plan: ok, or it fails.
  mkdir -p "$t/ab/bin" "$t/ab/host" "$t/ab/dir"
  cat > "$t/ab/bin/docker" << 'SH'
#!/usr/bin/env bash
h="$(dirname "$0")/../host"
case "$1 ${2:-}" in
  "image inspect") [ -f "$h/present" ] ;;
  build\ *)
    shift
    echo "build $*" >> "$h/calls"
    read -r step rest < "$h/plan" || true
    printf '%s\n' "$rest" > "$h/plan"
    echo "#7 [build 9/12] RUN CGO_ENABLED=0 GOOS=linux go build"
    [ "$step" = ok ] && : > "$h/present" ;;
  *) exit 3 ;;
esac
SH
  chmod +x "$t/ab/bin/docker"
  ab_case() { rm -f "$t/ab/host/present"; : > "$t/ab/host/calls"; printf '%s\n' "$1" > "$t/ab/host/plan"; [ -z "${2:-}" ] || : > "$t/ab/host/present"; }
  ab_run() { (PATH="$t/ab/bin:$PATH" DIR="$t/ab/dir" api_build "$aref" "$c") > "$t/ab/out" 2>&1; }
  # ab_built <builds>: the run passed after that many builds, each with the Containerfile and the
  # context of catalyst-build.yaml, the short commit, a build time and the name.
  ab_built() { ab_run && [ "$(grep -c . "$t/ab/host/calls")" = "$1" ] \
    && [ "$(sed -E 's/BUILD_TIME=[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z /BUILD_TIME=T /' "$t/ab/host/calls" | sort -u)" \
      = "build --progress=plain --provenance=false --sbom=false --file $ROOT/$API_CONTAINERFILE --build-arg BUILD_SHA=${c:0:7} --build-arg BUILD_TIME=T --tag $aref $ROOT" ]; }
  ab_none() { ab_run && [ ! -s "$t/ab/host/calls" ]; }
  ab_named() { [ "$(grep -c . "$t/ab/host/calls")" = "$API_IMAGE_ATTEMPTS" ] && grep -q -F 'the last step it reached: #7 [build 9/12] RUN' "$t/ab/out"; }
  ab_case "" present
  expect 0 "api_build: an image of the name on this host is used and nothing is built" ab_none
  ab_case ok
  expect 0 "api_build: an image not on this host is built once, as catalyst-build.yaml builds it" ab_built 1
  ab_case "fail ok"
  expect 0 "api_build: a failed attempt is followed by the next" ab_built 2
  ab_case "fail fail ok"
  expect 1 "api_build: when every attempt fails, api_build fails" ab_run
  expect 0 "api_build: ... after exactly the allowed attempts, naming the last build step it reached" ab_named

  # api_load and api_load_pack against a stand-in docker: each machine is a folder of $t/al/host
  # with its digest; import keeps the archive it reads and logs the base name, images ls name==
  # answers with the digest, tag adds a reference and images ls -q lists them. A machine with the
  # file noctr does not answer ctr, noimport fails the import, notag fails the tag.
  mkdir -p "$t/al/bin" "$t/al/dir"
  cat > "$t/al/bin/docker" << 'SH'
#!/usr/bin/env bash
h="$(dirname "$0")/../host"
case "$*" in
  "save "*) echo "archive of $2" ;;
  "exec -i "*" ctr -n k8s.io images import --all-platforms --base-name "*" -")
    [ ! -f "$h/$3/noimport" ] || exit 1
    cat > "$h/$3/image"; echo "import $3 ${12}" >> "$h/calls" ;;
  "exec "*" ctr -n k8s.io images ls name=="*)
    [ ! -f "$h/$2/noctr" ] || exit 1
    echo "REF TYPE DIGEST SIZE PLATFORMS LABELS"
    if [ -f "$h/$2/image" ]; then echo "${9#name==} application/vnd.oci.image.index.v1+json $(cat "$h/$2/digest") 90.1 MiB linux/amd64 -"; fi ;;
  "exec "*" ctr -n k8s.io images tag --force "*)
    [ ! -f "$h/$2/notag" ] || exit 1
    echo "${11}" >> "$h/$2/refs"; echo "tag $2 ${10} ${11}" >> "$h/calls" ;;
  "exec "*" ctr -n k8s.io images ls -q")
    [ ! -f "$h/$2/noctr" ] || exit 1
    cat "$h/$2/refs" 2> /dev/null || true ;;
  *) exit 3 ;;
esac
SH
  chmod +x "$t/al/bin/docker"
  local al_m
  mapfile -t al_m < <(machines "$t/v2.json")
  # al_case [<machine index> <file> [<content>]]: every machine holds the digest adig; then the
  # file of the machine is written (empty without a content).
  al_case() { local m; rm -rf "$t/al/host" "$t/al/dir"; mkdir -p "$t/al/host" "$t/al/dir"; : > "$t/al/host/calls"
    for m in "${al_m[@]}"; do mkdir -p "$t/al/host/$m"; echo "$adig" > "$t/al/host/$m/digest"; done
    [ $# = 0 ] || echo "${3:-}" > "$t/al/host/${al_m[$1]}/$2"; }
  al_one() { echo "archive of $aref" > "$t/al/dir/a.tar"; (PATH="$t/al/bin:$PATH" api_load "${al_m[0]}" "$aref" "$t/al/dir/a.tar") > "$t/al/out" 2> "$t/al/err"; }
  al_one_ok() { al_case; al_one && [ "$(cat "$t/al/out")" = "$adig" ] && [ "$(cat "$t/al/host/${al_m[0]}/image")" = "archive of $aref" ] \
    && [ "$(cat "$t/al/host/calls")" = "$(printf 'import %s %s\ntag %s %s %s' "${al_m[0]}" "$arepo" "${al_m[0]}" "$aref" "$arepo@$adig")" ]; }
  expect 0 "api_load: imports the archive under the repository's base name, tags it by digest and prints the digest" al_one_ok
  al_case 0 noimport
  expect 1 "api_load: a failed import fails" al_one
  expect 0 "api_load: ... naming the machine" grep -q -F "api_load: ${al_m[0]} did not import $aref" "$t/al/err"
  al_case 0 digest
  expect 1 "api_load: a machine that holds no digest for the image fails" al_one
  al_case 0 notag
  expect 1 "api_load: a machine that cannot tag it by digest fails" al_one
  al_pack() { (PATH="$t/al/bin:$PATH" DIR="$t/al/dir" API_LOAD_LIMIT=0 api_load_pack "$t/v2.json" "$aref") > "$t/al/out" 2> "$t/al/err"; }
  al_pack_ok() { al_case; : > "$t/al/dir/api-image.txt"; al_pack || return 1
    local m want=""
    for m in "${al_m[@]}"; do
      [ "$(cat "$t/al/host/$m/image")" = "archive of $aref" ] || return 1
      want+="  $m holds it as $arepo@$adig"$'\n'
    done
    [ "$(cat "$t/al/dir/api-digest")" = "$adig" ] && [ "$(cat "$t/al/dir/api-image.txt")" = "${want%$'\n'}" ] && [ ! -e "$t/al/dir/api-image.tar" ]; }
  expect 0 "api_load_pack: every machine of both regions holds the image under one digest, each named in api-image.txt" al_pack_ok
  al_case 2 digest "$pdig"
  expect 1 "api_load_pack: a machine that holds the image under another digest stops the run" al_pack
  expect 0 "api_load_pack: ... naming it and both digests" grep -q -F "api image: ${al_m[2]} holds $aref as $pdig, another machine as $adig" "$t/al/err"
  al_case 1 noctr
  expect 1 "api_load_pack: a machine whose k3s does not answer ctr stops the run" al_pack
  expect 0 "api_load_pack: ... naming it" grep -q -F "api image: the k3s of ${al_m[1]} does not answer ctr within 0s" "$t/al/err"

  # api_states against a stand-in docker: the Deployment and its pods come from $t/as/host; a
  # Deployment that is not there answers with the error in $t/as/host/dep.err.
  mkdir -p "$t/as/bin" "$t/as/host" "$t/as/dir/api"
  cat > "$t/as/bin/docker" << 'SH'
#!/usr/bin/env bash
h="$(dirname "$0")/../host"
case "$*" in
  *" -n catalyst-system get deployment catalyst-api -o json")
    if [ -f "$h/dep.json" ]; then cat "$h/dep.json"; else cat "$h/dep.err" >&2; exit 1; fi ;;
  *" -n catalyst-system get pods -l app.kubernetes.io/name=catalyst-api -o json") cat "$h/pods.json" ;;
  *) exit 3 ;;
esac
SH
  chmod +x "$t/as/bin/docker"
  as_run() { (PATH="$t/as/bin:$PATH" DIR="$t/as/dir" api_states region-b m1 "$aref" "$adig" "$1") > "$t/as/out" 2> "$t/as/err"; }
  as_absent() { rm -f "$t/as/host/dep.json"; echo "$1" > "$t/as/host/dep.err"; }
  # as_said <required> <start of the line>: api_states passes and its verdict file starts so.
  as_said() { local v; as_run "$1" && v=$(cat "$t/as/dir/api/region-b-api.txt") && [ "${v#"$2"}" != "$v" ]; }
  as_absent 'Error from server (NotFound): deployments.apps "catalyst-api" not found'
  expect 0 "api_states: a Deployment the API server says is NotFound, in a secondary region, is said and not judged" \
    as_said no 'no Deployment catalyst-system/catalyst-api in this region'
  expect 1 "api_states: ... in the primary region it waits" as_run yes
  as_absent 'Unable to connect to the server: dial tcp 172.30.2.10:6443: i/o timeout'
  expect 1 "api_states: a Deployment that cannot be read waits, also in a secondary region" as_run no
  expect 0 "api_states: ... with the error of the read in its verdict" grep -q -F 'waiting: the Deployment catalyst-system/catalyst-api cannot be read: Unable to connect' "$t/as/dir/api/region-b-api.txt"
  aj_dep 2 2 1 1 1 "$aref"
  aj_pods "$(aj_pod catalyst-api-abc-x1 catalyst-api-abc true "$arepo@$adig")"
  cp "$aj/dep.json" "$aj/pods.json" "$t/as/host/"
  as_runs() { as_said yes "runs $aref of the tree as $arepo@$adig" && cmp -s "$t/as/host/pods.json" "$t/as/dir/api/region-b-api-pods.json" \
    && cmp -s "$t/as/host/dep.json" "$t/as/dir/api/region-b-api-deployment.json"; }
  expect 0 "api_states: a Deployment that runs the image of the tree passes, its verdict and the objects it was read from kept" as_runs

  # tree_redirect with the tag of the image of the tree (the stand-in of tree_states above).
  rr_api() { : > "$t/ts/host/journal"; : > "$t/ts/host/root"; rm -f "$t/ts/host/kit" "$t/ts/host/fail-patch"
    echo "$atag flux-system" > "$t/ts/dir/api-tag"
    rr_run && [ "$(cat "$t/ts/host/journal")" = "patch $(tree_patch "$atag" flux-system < "$t/ts/dir/tree-charts.tsv")" ] \
      && grep -q -F "region-a: bootstrap-kit takes bp-x from the tree, and bp-catalyst-platform runs the catalyst-api image $API_REPO:$atag (sovereign-root patched" "$t/ts/out" || return 1
    local st=0
    : > "$t/ts/host/journal"; mv "$t/ts/dir/tree-charts.tsv" "$t/ts/dir/tree-charts.aside"; : > "$t/ts/dir/tree-charts.tsv"
    if ! rr_run || [ "$(cat "$t/ts/host/journal")" != "patch $(tree_patch "$atag" flux-system < /dev/null)" ] \
      || ! grep -q -F "region-a: bootstrap-kit takes no chart from the tree, and bp-catalyst-platform runs the catalyst-api image $API_REPO:$atag" "$t/ts/out"; then
      st=1
    fi
    mv "$t/ts/dir/tree-charts.aside" "$t/ts/dir/tree-charts.tsv"; rm -f "$t/ts/dir/api-tag"
    return "$st"; }
  expect 0 "tree_redirect: with the tag of the image of the tree the entry also gives the console release that image, with or without a chart of the tree" rr_api

  echo "self-test: $n cases, $fail failed"
  [ "$fail" = 0 ]
}

case "${1:-}" in
  image) cmd_image ;;
  charts) shift; cmd_charts "$@" ;;
  up) shift; cmd_up "$@" ;;
  discovery) cmd_discovery ;;
  claim) cmd_claim ;;
  collect) cmd_collect ;;
  down) cmd_down ;;
  --self-test) self_test ;;
  *) usage ;;
esac
