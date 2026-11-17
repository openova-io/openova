#!/usr/bin/env bash
# Step 03 copies the sandbox (pause) image the nodes are configured to pull.
#
# Measured on hw307 (deployment 9836870916c31984, 16-17 Nov 2026): worker
# w26b2e6 cannot create any Pod sandbox —
#   failed to get sandbox image "rancher/mirrored-pause:3.6": failed to pull and
#   unpack image "docker.io/rancher/mirrored-pause:3.6": ... unexpected media
#   type text/html
# The local registry answers 404 for that image at the docker.io mirror path
# the registry pivot writes (proxy-dockerhub). The image is in no Pod spec, no
# workload template, no chart render and no kit manifest — the four sources
# step 03 walked — because containerd starts every Pod with the image the node
# is configured with: k3s --pause-image, else the default of the node's k3s
# release. The repo's node templates pass no --pause-image, so hw307's nodes
# run k3s v1.31.8+k3s1's default. Every kubelet reports that image on each
# Pod's sandbox (cadvisor series with container="" and a pod); on hw307 all
# twelve nodes report image="docker.io/rancher/mirrored-pause:3.6".
#
# Every case EXECUTES the shell the chart renders (sliced out of the rendered
# step-03 script) against a stubbed kubectl serving that kubelet output, so the
# image the nodes report is the one the test expects in the prewarm list —
# nothing here or in the chart names a pause image of its own.
#
# Usage: bash tests/step03-node-sandbox-image.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="sov.example.test"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}
python3 - "${TMP}/render.yaml" "${TMP}/s03.sh" <<'PY'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "ConfigMap" and d["metadata"]["name"] == "cutover-step-03-harbor-prewarm":
        open(sys.argv[2], "w").write(d["data"]["run.sh"])
PY
S03="${TMP}/s03.sh"
[ -s "${S03}" ] || { echo "FAIL: could not extract the step-03 run.sh from the render"; exit 1; }
block() { awk -v a="$2" 'index($0, a" BEGIN") {f=1; next} index($0, a" END") {exit} f {print}' "$1"; }
block "${S03}" "prewarm-list-images" >"${TMP}/list.sh"
block "${S03}" "prewarm-run-guard" >"${TMP}/guard.sh"
grep -q '^prewarm_list_images() {' "${TMP}/list.sh" || { echo "FAIL: the rendered step-03 script has no prewarm_list_images block"; exit 1; }
grep -q '^prewarm_run_guard() {' "${TMP}/guard.sh" || { echo "FAIL: the rendered step-03 script has no run guard block"; exit 1; }

# ── fixtures: two regions, the kubelet output in the shape hw307's kubelets serve ──
FIX="${TMP}/fix"; mkdir -p "${FIX}/kc"
: >"${FIX}/kc/hw-x-b-rtz-prod.yaml"
PAUSE_A="docker.io/rancher/mirrored-pause:3.6"   # what hw307's k3s v1.31.8+k3s1 nodes report
PAUSE_B="registry.k8s.io/pause:3.10"             # a node started with k3s --pause-image=registry.k8s.io/pause:3.10
pods_json() { python3 -c 'import json,sys; print(json.dumps({"items":[{"spec":{"containers":[{"image":i}]}} for i in sys.argv[1:]]}))' "$@"; }
nodes_json() { python3 -c 'import json,sys; print(json.dumps({"items":[{"metadata":{"name":n}} for n in sys.argv[1:]]}))' "$@"; }
pods_json docker.io/app/api:1 >"${FIX}/primary-pods.json"
pods_json quay.io/only-b/x:2 >"${FIX}/hw-x-b-rtz-prod-pods.json"
echo '{"items":[]}' >"${FIX}/primary-wl.json"
echo '{"items":[]}' >"${FIX}/hw-x-b-rtz-prod-wl.json"
nodes_json n-a1 n-a2 n-a3 >"${FIX}/primary-nodes.json"
nodes_json n-b1 >"${FIX}/hw-x-b-rtz-prod-nodes.json"
# cadvisor <sandbox image>: a sandbox (container="", a pod), an app container
# whose image is in no Pod spec (must NOT be taken: only sandboxes are), a Pod
# cgroup and the root cgroup (no image).
cadvisor() {
  cat <<EOF
# HELP container_last_seen Last time a container was seen by the exporter
# TYPE container_last_seen gauge
container_last_seen{container="",id="/kubepods/burstable/pod11/0a1b",image="$1",name="0a1b",namespace="kube-system",pod="hubble-relay-744bdfc87-b9mt2"} 1.794926794e+09 1794926794940
container_last_seen{container="hubble-relay",id="/kubepods/burstable/pod11/2c3d",image="docker.io/only/in-metrics:1",name="2c3d",namespace="kube-system",pod="hubble-relay-744bdfc87-b9mt2"} 1.794926794e+09 1794926794940
container_last_seen{container="",id="/kubepods/burstable/pod11",image="",name="",namespace="kube-system",pod="hubble-relay-744bdfc87-b9mt2"} 1.794926795e+09 1794926795160
container_last_seen{container="",id="/",image="",name="",namespace="",pod=""} 1.794926795e+09 1794926795139
container_start_time_seconds{container="",id="/kubepods/burstable/pod11/0a1b",image="$1",name="0a1b",namespace="kube-system",pod="hubble-relay-744bdfc87-b9mt2"} 1.794926e+09 1794926794940
EOF
}
cadvisor "${PAUSE_A}" >"${FIX}/n-a1.metrics"
cadvisor "${PAUSE_A}" >"${FIX}/n-a2.metrics"
touch "${FIX}/n-a3.fail"                          # a node whose kubelet does not answer
cadvisor "${PAUSE_B}" >"${FIX}/n-b1.metrics"
stub_kubectl() {
  kubectl() {
    local r=primary
    if [ "${1:-}" = "--kubeconfig" ]; then r=$(basename "$2" .yaml); shift 2; fi
    case "$*" in
      "get pods -A -o json") cat "${FIX}/${r}-pods.json" ;;
      "get deployments,statefulsets,daemonsets,cronjobs,jobs -A -o json") cat "${FIX}/${r}-wl.json" ;;
      "get nodes -o json") cat "${FIX}/${r}-nodes.json" ;;
      "auth can-i get nodes --subresource=proxy") [ ! -e "${FIX}/${r}-noproxy" ] ;;
      "get --raw /api/v1/nodes/"*"/proxy/metrics/cadvisor")
        local n="${3#/api/v1/nodes/}"; n="${n%%/*}"
        [ -e "${FIX}/${n}.fail" ] && { echo "Error from server (ServiceUnavailable)" >&2; return 1; }
        cat "${FIX}/${n}.metrics" ;;
      *) echo "unexpected kubectl $*" >&2; return 9 ;;
    esac
  }
}

echo "[step03-sandbox] A: the walk names the sandbox image every region's nodes report"
(
  set -eu
  stub_kubectl
  # shellcheck source=/dev/null
  . "${TMP}/list.sh"
  prewarm_list_images "" primary
  prewarm_list_images "${FIX}/kc/hw-x-b-rtz-prod.yaml" hw-x-b-rtz-prod
) >"${TMP}/a.tsv" 2>"${TMP}/a.err" || fail "the walk failed: $(tail -2 "${TMP}/a.err")"
grep -qxF "${PAUSE_A}	primary" "${TMP}/a.tsv" \
  && pass "${PAUSE_A}, the sandbox image of this region's nodes, is in the prewarm list" \
  || fail "${PAUSE_A}, the sandbox image this region's nodes are configured with, is NOT in the prewarm list: $(tr '\n' ' ' <"${TMP}/a.tsv")"
grep -qxF "${PAUSE_B}	hw-x-b-rtz-prod" "${TMP}/a.tsv" \
  && pass "${PAUSE_B}, the sandbox image a follower node is started with, is in the list with its region" \
  || fail "the follower node's sandbox image ${PAUSE_B} is NOT in the prewarm list: $(tr '\n' ' ' <"${TMP}/a.tsv")"
grep -qxF "docker.io/app/api:1	primary" "${TMP}/a.tsv" && grep -qxF "quay.io/only-b/x:2	hw-x-b-rtz-prod" "${TMP}/a.tsv" \
  && pass "the images of the Pod specs are still listed" \
  || fail "the Pod-spec images are no longer listed: $(tr '\n' ' ' <"${TMP}/a.tsv")"
grep -q 'only/in-metrics' "${TMP}/a.tsv" && fail "an application container's image was read from the kubelet as a sandbox image" || pass "only sandbox series are read (an application container's image is not)"
[ "$(grep -c . "${TMP}/a.tsv")" = 4 ] && pass "each image once per region (4 lines)" || fail "expected 4 lines, got: $(tr '\n' ' ' <"${TMP}/a.tsv")"
grep -q 'no kubelet answer from n-a3' "${TMP}/a.err" && pass "a node whose kubelet does not answer is named" || fail "the unread node is not named: $(cat "${TMP}/a.err")"

echo "[step03-sandbox] B: a region none of whose nodes report a sandbox image fails the walk"
touch "${FIX}/n-b1.fail"
(
  set -eu
  stub_kubectl
  # shellcheck source=/dev/null
  . "${TMP}/list.sh"
  prewarm_list_images "${FIX}/kc/hw-x-b-rtz-prod.yaml" hw-x-b-rtz-prod
) >"${TMP}/b.tsv" 2>"${TMP}/b.err" && fail "the walk passed although no node of the region could be read" \
  || { grep -q 'FATAL: no node of the region reported the sandbox image' "${TMP}/b.err" && pass "it fails, naming why" || fail "it failed without naming why: $(cat "${TMP}/b.err")"; }
rm -f "${FIX}/n-b1.fail"

echo "[step03-sandbox] E: under an installed ClusterRole older than this step (a release delivery runs the candidate's step 03 with the installed grants, #7960) the walk names the missing grant and goes on"
touch "${FIX}/primary-noproxy"
(
  set -eu
  stub_kubectl
  # shellcheck source=/dev/null
  . "${TMP}/list.sh"
  prewarm_list_images "" primary
) >"${TMP}/e.tsv" 2>"${TMP}/e.err" || fail "the walk failed under the older grants, so the delivery that installs the grant could never run: $(tail -2 "${TMP}/e.err")"
grep -q 'may not get nodes/proxy' "${TMP}/e.err" && pass "it names the missing nodes/proxy grant" || fail "it does not name the missing grant: $(cat "${TMP}/e.err")"
grep -qxF "docker.io/app/api:1	primary" "${TMP}/e.tsv" && pass "the Pod-spec images are still listed" || fail "the Pod-spec images are missing: $(tr '\n' ' ' <"${TMP}/e.tsv")"
rm -f "${FIX}/primary-noproxy"

echo "[step03-sandbox] C: the run guard copies a missing sandbox image once, then fails by name"
run_guard() {
  local dir="${TMP}/g-$1"; shift
  mkdir -p "${dir}"
  printf '%s\n' "$@" >"${dir}/present"
  touch "${dir}/heal"
  : >"${dir}/seed.tsv"
  (
    set -eu
    stub_kubectl
    cpdir="${dir}/phase-a"; mkdir -p "${cpdir}"
    offlinemirror_local_paths() {
      case "$1" in
        docker.io/*) printf 'proxy-dockerhub/%s\n' "${1#docker.io/}" ;;
        quay.io/*) printf 'proxy-quay/%s\n' "${1#quay.io/}" ;;
        registry.k8s.io/*) printf 'proxy-k8s/%s\n' "${1#registry.k8s.io/}" ;;
        *) printf '__UNMAPPED__ %s\n' "${1%%/*}" ;;
      esac
    }
    harbor_durable_digest() { grep -qxF "$1" "${dir}/present" && printf 'sha256:0'; return 0; }
    copy_one() {
      echo "copy_one $1" >>"${dir}/copies"
      local p; p=$(offlinemirror_local_paths "$1")
      grep -qxF "${p}" "${dir}/heal" && echo "${p}" >>"${dir}/present"
      return 0
    }
    # shellcheck source=/dev/null
    . "${TMP}/list.sh"
    # shellcheck source=/dev/null
    . <(sed -e "s#/secondary-kubeconfigs/#${FIX}/kc/#g" "${TMP}/guard.sh")
    prewarm_run_guard "${dir}/work" "${dir}/seed.tsv"
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}
run_guard miss proxy-dockerhub/app/api:1 proxy-quay/only-b/x:2
G="${TMP}/g-miss"
[ "$(cat "${G}/rc")" != 0 ] && pass "the guard fails the step" || fail "the guard passed although the registry lacks the nodes' sandbox images"
grep -q "RUNNING-MISSING ${PAUSE_A} -> proxy-dockerhub/rancher/mirrored-pause:3.6 (runs in: primary)" "${G}/out" \
  && pass "it names ${PAUSE_A} and the path the nodes pull it from" \
  || fail "it does not name ${PAUSE_A}: $(grep -m3 -e RUNNING -e FATAL "${G}/out" | cut -c1-200)"
grep -q "RUNNING-MISSING ${PAUSE_B} -> proxy-k8s/pause:3.10 (runs in: hw-x-b-rtz-prod)" "${G}/out" \
  && pass "it names the follower node's sandbox image with its region" \
  || fail "it does not name ${PAUSE_B}"
grep -qxF "copy_one ${PAUSE_A}" "${G}/copies" 2>/dev/null && pass "the missing sandbox image is copied once before the guard decides" || fail "the guard did not try to copy ${PAUSE_A}"
mkdir -p "${TMP}/g-heal"
printf 'proxy-dockerhub/rancher/mirrored-pause:3.6\nproxy-k8s/pause:3.10\n' >"${TMP}/g-heal/heal"
run_guard heal proxy-dockerhub/app/api:1 proxy-quay/only-b/x:2
[ "$(cat "${TMP}/g-heal/rc")" = 0 ] && grep -q '(2 copied by the guard)' "${TMP}/g-heal/out" \
  && pass "once copied, the guard passes and counts both" \
  || fail "the guard did not pass after copying (rc $(cat "${TMP}/g-heal/rc")): $(tail -2 "${TMP}/g-heal/out" | cut -c1-200)"

echo "[step03-sandbox] D: the step's ServiceAccount may read the kubelet (a stub can never be refused by RBAC)"
python3 - "${TMP}/render.yaml" <<'PY' && pass "the runner ClusterRole grants get on nodes/proxy and list on nodes" || fail "the runner ClusterRole lacks get on nodes/proxy or list on nodes: the live walk would be refused"
import sys, yaml
ok_proxy = ok_nodes = False
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "ClusterRole":
        for r in d.get("rules") or []:
            if "" in (r.get("apiGroups") or []):
                if "nodes/proxy" in r.get("resources", []) and "get" in r.get("verbs", []):
                    ok_proxy = True
                if "nodes" in r.get("resources", []) and "list" in r.get("verbs", []):
                    ok_nodes = True
sys.exit(0 if ok_proxy and ok_nodes else 1)
PY

sh -n "${S03}" 2>"${TMP}/syntax.err" && pass "the rendered step-03 script parses as POSIX sh" || fail "the rendered step-03 script does not parse: $(head -2 "${TMP}/syntax.err")"

echo
if [ "${FAILURES}" -gt 0 ]; then
  echo "[step03-sandbox] FAILURES=${FAILURES}"; exit 1
fi
echo "[step03-sandbox] all cases passed"
