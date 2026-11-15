#!/usr/bin/env bash
# #7922 — step 03 (harbor-prewarm) uses the parent's proxy, and ends only when
# every image the Sovereign runs is in its own registry.
#
# Measured on hw307 (deployment 9836870916c31984, 15 Nov 2026, step 03
# 17:38:27-18:19:52Z): 77 images were copied "via DIRECT upstream (mothership
# proxy path failing)". The first was emberstack/kubernetes-reflector:7.1.288:
# the parent's proxy served its index but answered `manifest unknown` for one
# child manifest (17:41:34Z), the direct copy worked, and copy_one latched the
# proxy DOWN for the rest of the wave. 75 later proxy-sourced images went to
# Docker Hub, ghcr.io, quay.io and registry.k8s.io without trying the proxy,
# while the proxy kept answering. A second proxy leg (alpine/k8s:1.31.4,
# 17:50:33Z) failed on the DESTINATION (`writing blob: Patch
# "https://registry.<fqdn>/v2/…/blobs/uploads/…": use of closed network
# connection`) and was read as a proxy failure. The Job log carried no line
# that said the proxy was latched, or why; the per-image error reached it only
# in the tally, twenty minutes later.
#
# Separately, no guard checked an image that only a follower region runs (the
# follower leg keeps ghcr.io/openova-io/* only), and the #7900 catalogue-only
# warning did not ask whether a pod runs the image.
#
# Every case below EXECUTES the shell the chart renders (sliced out of the
# rendered step-03 script), with stubbed skopeo, curl and kubectl. Each fails on
# the tree before the repair.
#
# Usage: bash tests/step7922-prewarm-proxy-path.sh [CHART_DIR]
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

# fn <script> <name>: print the shell function <name> (column-0 definition).
fn() { awk -v n="$2" '$0 ~ "^"n"\\(\\) \\{" {f=1} f {print} f && (/^\}$/ || $0 ~ "^"n"\\(\\) \\{.*\\}$") {exit}' "$1"; }
# block <script> <anchor>: the lines between "<anchor> BEGIN" and "<anchor> END".
block() { awk -v a="$2" 'index($0, a" BEGIN") {f=1; next} index($0, a" END") {exit} f {print}' "$1"; }

# ─────────────────────────────────────────────────────────── the copy worker
for f in rl_slug prewarm_multiarch_flags prewarm_skopeo_copy mothership_proxy_code copy_one; do
  fn "${S03}" "${f}" >>"${TMP}/copy.sh"
done
for f in rl_slug prewarm_multiarch_flags prewarm_skopeo_copy copy_one; do
  grep -q "^${f}() {" "${TMP}/copy.sh" || fail "could not slice ${f} out of the rendered step-03 script"
done

PROXY="proxy.parent.test"
EMB="${PROXY}/proxy-dockerhub/emberstack/kubernetes-reflector:7.1.288"
K8S="${PROXY}/proxy-dockerhub/alpine/k8s:1.31.4"
# run_wave <dir> <probe code> <image>... : runs the REAL copy_one for each image
# in turn, in ONE wave (one cpdir). The skopeo stub fails a copy whose source
# is listed in <dir>/fail (one "source<TAB>count<TAB>message" per line) as many
# times as the count says (0 = always), and succeeds otherwise. Every copy's
# source is recorded in order in <dir>/calls; stdout (the Job log) in <dir>/out.
run_wave() {
  local dir="$1" code="$2"; shift 2
  mkdir -p "${dir}/cp"; : >"${dir}/calls"; touch "${dir}/fail"
  (
    set -eu
    cpdir="${dir}/cp"
    MOTHERSHIP_HOST="${PROXY}"
    HOST_PROJECT_MAP="docker.io:proxy-dockerhub quay.io:proxy-quay"
    HARBOR_HOST="registry.${FQDN}"
    HARBOR_USERNAME=u; HARBOR_PASSWORD=p
    PREWARM_DEST_TLS_VERIFY=false; PREWARM_SKOPEO_TIMEOUT=1s
    PREWARM_COPY_ATTEMPTS=3; PREWARM_PROXY_COPY_ATTEMPTS=3; PREWARM_PROXY_BACKOFF_BASE_SECONDS=0
    PROXY_DIRECT_FIRST_ENABLED=true; PROXY_PROBE_TIMEOUT_SECONDS=1
    offlinemirror_normalize_ref() { printf '%s' "$1"; }
    offlinemirror_local_paths() { printf '%s' "${1#*/}"; }
    src_creds_for() { printf ''; }
    dest_already_current() { return 1; }
    harbor_durable_digest() { printf ''; }
    sleep() { :; }
    curl() { echo "probe" >>"${dir}/probes"; printf '%s' "${code}"; }
    skopeo() {
      local a src="" prev="" line s n msg left
      case " $* " in *" inspect "*) return 1 ;; esac
      for a in "$@"; do src="${prev}"; prev="${a}"; done
      src="${src#docker://}"
      echo "${src}" >>"${dir}/calls"
      line=$(awk -F'\t' -v s="${src}" '$1==s' "${dir}/fail")
      [ -n "${line}" ] || return 0
      n=$(printf '%s' "${line}" | cut -f2); msg=$(printf '%s' "${line}" | cut -f3-)
      left=$(grep -cxF "${src}" "${dir}/calls")
      if [ "${n}" = 0 ] || [ "${left}" -le "${n}" ]; then
        echo "time=\"x\" level=fatal msg=\"${msg}\""
        return 1
      fi
      return 0
    }
    # shellcheck source=/dev/null
    . "${TMP}/copy.sh"
    for img in "$@"; do copy_one "${img}"; done
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}

echo "[step7922] A: a destination error inside a proxy leg is the destination's, not the proxy's (alpine/k8s:1.31.4, 17:50:33Z)"
D="${TMP}/a"; mkdir -p "${D}"
printf '%s\t1\t%s\n' "${K8S}" "copying image 1/2 from manifest list: writing blob: Patch \\\"https://registry.${FQDN}/v2/proxy-dockerhub/alpine/k8s/blobs/uploads/26ba2458?_state=x\\\": use of closed network connection" >"${D}/fail"
run_wave "${D}" 401 "${K8S}"
if [ "$(sed -n 2p "${D}/calls")" = "${K8S}" ]; then
  pass "the retry reads the SAME source, the parent's proxy"
else
  fail "the retry went to '$(sed -n 2p "${D}/calls")' — a destination error was read as a failure of the proxy"
fi
[ -e "${D}/cp/.proxy_down" ] && fail "the proxy was latched DOWN on a destination error" || pass "the proxy is not latched down"
grep -q 'DIRECT upstream' "${D}/out" && fail "the Job log blames the proxy: $(grep 'DIRECT upstream' "${D}/out" | head -1 | cut -c1-200)" || pass "the Job log does not blame the proxy"
[ "$(cat "${D}/rc")" = 0 ] && ls "${D}/cp/"*.ok >/dev/null 2>&1 && pass "the image is copied on the retry" || fail "the image was not copied: $(tail -2 "${D}/out")"

echo "[step7922] B: the proxy answers but cannot serve ONE image (emberstack, 17:41:34Z) — that image goes direct, the proxy is NOT latched"
D="${TMP}/b"; mkdir -p "${D}"
printf '%s\t0\t%s\n' "${EMB}" "copying image 1/3 from manifest list: determining manifest MIME type for docker://${EMB}: reading manifest sha256:0b4ff27e in ${PROXY}/proxy-dockerhub/emberstack/kubernetes-reflector: manifest unknown" >"${D}/fail"
run_wave "${D}" 401 "${EMB}" "${K8S}"
[ "$(sed -n 2p "${D}/calls")" = "docker.io/emberstack/kubernetes-reflector:7.1.288" ] \
  && pass "emberstack: after the proxy's answer the direct upstream is tried" \
  || fail "emberstack: no direct fallback (calls: $(tr '\n' ' ' <"${D}/calls"))"
[ -e "${D}/cp/.proxy_down" ] && fail "the proxy was latched DOWN although its registry API answers (HTTP 401) — every later image of the wave goes direct" || pass "the proxy is not latched down"
first_k8s=$(grep -F 'alpine/k8s:1.31.4' "${D}/calls" | head -1)
[ "${first_k8s}" = "${K8S}" ] \
  && pass "the NEXT image of the wave is read from the proxy first" \
  || fail "the next image of the wave went to '${first_k8s}' first instead of the proxy"
grep -F 'succeeded via DIRECT upstream docker.io/emberstack/kubernetes-reflector:7.1.288' "${D}/out" | grep -qF 'manifest unknown' \
  && pass "the Job log line for emberstack names the proxy's error" \
  || fail "the Job log does not say why emberstack went direct: $(grep 'DIRECT upstream' "${D}/out" | head -1 | cut -c1-220)"
grep -q 'NOT latched' "${D}/out" && pass "the Job log says the proxy stays in use" || fail "the Job log does not say the proxy stays in use"
[ "$(cat "${D}/rc")" = 0 ] || fail "the wave failed: $(tail -2 "${D}/out")"

echo "[step7922] C: the proxy path is down — latched, and the Job log says by which image and why"
D="${TMP}/c"; mkdir -p "${D}"
printf '%s\t0\t%s\n' "${EMB}" "initializing source docker://${EMB}: pinging container registry ${PROXY}: Get \\\"https://${PROXY}/v2/\\\": dial tcp 203.0.113.7:443: i/o timeout" >"${D}/fail"
run_wave "${D}" 000 "${EMB}" "${K8S}"
[ -e "${D}/cp/.proxy_down" ] && pass "the proxy is latched down" || fail "the proxy was not latched down although its registry API does not answer"
if grep -F 'proxy LATCHED DOWN by' "${D}/out" | grep -F "${EMB}" | grep -qF 'i/o timeout'; then
  pass "the Job log names the image that latched the proxy and the proxy's error"
else
  fail "the Job log does not say which image latched the proxy, or why"
fi
first_k8s=$(grep -F 'alpine/k8s:1.31.4' "${D}/calls" | head -1)
[ "${first_k8s}" = "docker.io/alpine/k8s:1.31.4" ] \
  && pass "the next image goes direct-first (no timeout tax on a dead proxy)" \
  || fail "the next image went to '${first_k8s}' first"

# ───────────────────────────────────────────────── the walk of every region
echo "[step7922] D: Phase A reads the images of every region, not only its own"
block "${S03}" "prewarm-list-images" >"${TMP}/list.sh"
grep -q '^prewarm_list_images() {' "${TMP}/list.sh" || fail "the rendered step-03 script has no prewarm_list_images block"
awk '/^run_images_file=\/tmp\/\.prewarm-run-images\.tsv$/ {f=1} f {print} f && /run only in a follower region/ {exit}' "${S03}" >"${TMP}/union.sh"
grep -q 'secondary-kubeconfigs' "${TMP}/union.sh" || fail "the Phase A walk does not read the follower regions"

FIX="${TMP}/fix"; mkdir -p "${FIX}/kc"
: >"${FIX}/kc/hw-x-b-rtz-prod.yaml"
pods_json() { python3 -c 'import json,sys; print(json.dumps({"items":[{"spec":{"containers":[{"image":i}]}} for i in sys.argv[1:]]}))' "$@"; }
wl_json() { python3 -c 'import json,sys; print(json.dumps({"items":[{"spec":{"jobTemplate":{"spec":{"template":{"spec":{"containers":[{"image":i}]}}}}}} for i in sys.argv[1:]]}))' "$@"; }
pods_json docker.io/app/api:1 docker.io/litmus/portal:3 >"${FIX}/primary-pods.json"
wl_json docker.io/app/cron:1 >"${FIX}/primary-wl.json"
pods_json quay.io/only-b/x:2 docker.io/app/api:1 >"${FIX}/hw-x-b-rtz-prod-pods.json"
wl_json >"${FIX}/hw-x-b-rtz-prod-wl.json"
stub_kubectl() {
  kubectl() {
    local r=primary
    if [ "${1:-}" = "--kubeconfig" ]; then r=$(basename "$2" .yaml); shift 2; fi
    [ -e "${FIX}/${r}-fail" ] && return 1
    case "$*" in
      "get pods -A -o json") cat "${FIX}/${r}-pods.json" ;;
      "get deployments,statefulsets,daemonsets,cronjobs,jobs -A -o json") cat "${FIX}/${r}-wl.json" ;;
      *) echo "unexpected kubectl $*" >&2; return 9 ;;
    esac
  }
}
(
  set -eu
  stub_kubectl
  # shellcheck source=/dev/null
  . "${TMP}/list.sh"
  # shellcheck source=/dev/null
  . <(sed -e "s#/tmp/#${TMP}/e-#g" -e "s#/secondary-kubeconfigs/#${FIX}/kc/#g" "${TMP}/union.sh")
  printf '%s\n' ${openova_images} >"${TMP}/e.list"
) >"${TMP}/e.out" 2>&1 || fail "the walk failed: $(tail -2 "${TMP}/e.out")"
grep -qxF 'quay.io/only-b/x:2' "${TMP}/e.list" 2>/dev/null \
  && pass "an image only the follower region runs is in the Phase A list" \
  || fail "an image only the follower region runs is NOT in the Phase A list ($(tr '\n' ' ' <"${TMP}/e.list" 2>/dev/null))"
[ "$(grep -c . "${TMP}/e.list" 2>/dev/null)" = 4 ] && pass "the list holds each image once (4)" || fail "the list is not the union of both regions: $(tr '\n' ' ' <"${TMP}/e.list" 2>/dev/null)"
grep -q '1 of them run only in a follower region' "${TMP}/e.out" && pass "the Job log counts the follower-only images" || fail "the Job log does not count the follower-only images: $(tail -1 "${TMP}/e.out")"

# ────────────────────────────────────────────────────────────── the run guard
echo "[step7922] E: the run guard fails, by name, on an image a pod runs that is not in the local registry"
block "${S03}" "prewarm-run-guard" >"${TMP}/guard.sh"
if ! grep -q '^prewarm_run_guard() {' "${TMP}/guard.sh"; then
  fail "the rendered step-03 script has no run guard (prewarm_run_guard between its anchors)"
else
  [ "$(grep -v '^[[:space:]]*$' "${S03}" | tail -1)" = 'prewarm_run_guard /tmp/prewarm-run-guard "${run_images_file}" || exit 1' ] \
    && pass "the run guard is the last thing the step does, and the step fails with it" \
    || fail "the step does not end with the run guard and its exit: '$(grep -v '^[[:space:]]*$' "${S03}" | tail -1 | cut -c1-120)'"
  # run_guard <name> <present local paths...>: the seed lists what Phase A read.
  # copy_one marks a path present when <name>/heal lists it.
  run_guard() {
    local dir="${TMP}/g-$1"; shift
    mkdir -p "${dir}"
    printf '%s\n' "$@" >"${dir}/present"
    touch "${dir}/heal"
    (
      set -eu
      stub_kubectl
      cpdir="${dir}/phase-a"
      offlinemirror_local_paths() {
        case "$1" in
          docker.io/*) printf 'proxy-dockerhub/%s\n' "${1#docker.io/}" ;;
          quay.io/*) printf 'proxy-quay/%s\n' "${1#quay.io/}" ;;
          xpkg.upbound.io/*) : ;;
          *) printf '__UNMAPPED__ %s\n' "${1%%/*}" ;;
        esac
      }
      harbor_durable_digest() { grep -qxF "$1" "${dir}/present" && printf 'sha256:0'; return 0; }
      copy_one() {
        echo "copy_one $1 cpdir=${cpdir}" >>"${dir}/copies"
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
  mkdir -p "${TMP}/g-miss/phase-a"
  touch "${TMP}/g-miss/phase-a/.proxy_down"
  printf 'docker.io/litmus/other:9\tprimary\n' >"${TMP}/g-miss/seed.tsv"
  run_guard miss proxy-dockerhub/app/api:1 proxy-dockerhub/app/cron:1 proxy-dockerhub/litmus/other:9
  G="${TMP}/g-miss"
  [ "$(cat "${G}/rc")" != 0 ] && pass "the guard fails the step" || fail "the guard passed although two images that pods run are missing"
  grep -q 'RUNNING-MISSING docker.io/litmus/portal:3 -> proxy-dockerhub/litmus/portal:3 (runs in: primary)' "${G}/out" \
    && pass "it names the image of a catalogue-only chart that a pod runs anyway (the #7900 warning does not cover that)" \
    || fail "it does not name docker.io/litmus/portal:3: $(grep -m2 -e RUNNING -e FATAL "${G}/out" | cut -c1-200)"
  grep -q 'RUNNING-MISSING quay.io/only-b/x:2 -> proxy-quay/only-b/x:2 (runs in: hw-x-b-rtz-prod)' "${G}/out" \
    && pass "it names the image only the follower region runs, with its region" \
    || fail "it does not name quay.io/only-b/x:2 with its region"
  grep -q 'docker.io/app/api:1' "${G}/out" && fail "it names an image that is present" || pass "a present image (run in both regions) is not named"
  grep -q 'copy_one docker.io/litmus/portal:3' "${G}/copies" 2>/dev/null && pass "a missing image is copied once before the guard decides" || fail "the guard did not try to copy the missing image"
  grep -q "cpdir=${G}/work/cp" "${G}/copies" 2>/dev/null && pass "the guard's copies use their own sentinel directory" || fail "the guard's copies write into Phase A's sentinel directory"
  [ -e "${G}/work/cp/.proxy_down" ] && pass "a proxy latched DOWN in Phase A stays latched for the guard's copies" || fail "the guard's copies do not inherit Phase A's proxy latch (each would re-pay a dead proxy's timeout)"

  echo "[step7922] F: an image the guard copies is counted present"
  mkdir -p "${TMP}/g-heal"; : >"${TMP}/g-heal/seed.tsv"
  printf 'proxy-dockerhub/litmus/portal:3\nproxy-quay/only-b/x:2\n' >"${TMP}/g-heal/heal"
  run_guard heal proxy-dockerhub/app/api:1 proxy-dockerhub/app/cron:1
  [ "$(cat "${TMP}/g-heal/rc")" = 0 ] && grep -q 'run guard (#7922): PASS' "${TMP}/g-heal/out" && grep -q '(2 copied by the guard)' "${TMP}/g-heal/out" \
    && pass "both copied, the guard passes and says it copied two" \
    || fail "the guard did not pass after copying (rc $(cat "${TMP}/g-heal/rc")): $(tail -2 "${TMP}/g-heal/out" | cut -c1-200)"

  echo "[step7922] G: an image nothing runs stays out of the guard; an excluded one is named; an unreadable region fails"
  mkdir -p "${TMP}/g-ok"; printf 'xpkg.upbound.io/upbound/provider-opentofu:v1.1.3\tprimary\n' >"${TMP}/g-ok/seed.tsv"
  run_guard ok proxy-dockerhub/app/api:1 proxy-dockerhub/app/cron:1 proxy-dockerhub/litmus/portal:3 proxy-quay/only-b/x:2
  [ "$(cat "${TMP}/g-ok/rc")" = 0 ] && pass "every image that pods run is present: PASS (catalogue-only images nothing runs are not part of the run set)" || fail "the guard failed with every run image present: $(tail -2 "${TMP}/g-ok/out")"
  grep -q 'EXCLUDED xpkg.upbound.io/upbound/provider-opentofu:v1.1.3' "${TMP}/g-ok/out" && pass "an excluded image is named on its own line, not counted present" || fail "an excluded image is not named"
  touch "${FIX}/hw-x-b-rtz-prod-fail"
  mkdir -p "${TMP}/g-blind"; : >"${TMP}/g-blind/seed.tsv"
  run_guard blind proxy-dockerhub/app/api:1 proxy-dockerhub/app/cron:1 proxy-dockerhub/litmus/portal:3 proxy-quay/only-b/x:2
  rm -f "${FIX}/hw-x-b-rtz-prod-fail"
  [ "$(cat "${TMP}/g-blind/rc")" != 0 ] && grep -q 'could not list the images of follower region hw-x-b-rtz-prod' "${TMP}/g-blind/out" \
    && pass "a region whose images cannot be read fails the guard, naming the region" \
    || fail "the guard passed blind on a region it could not read (rc $(cat "${TMP}/g-blind/rc"))"
  mkdir -p "${TMP}/g-unmapped"; printf 'other.example/x/y:1\tprimary\n' >"${TMP}/g-unmapped/seed.tsv"
  run_guard unmapped proxy-dockerhub/app/api:1 proxy-dockerhub/app/cron:1 proxy-dockerhub/litmus/portal:3 proxy-quay/only-b/x:2
  [ "$(cat "${TMP}/g-unmapped/rc")" != 0 ] && grep -q 'RUNNING-MISSING other.example/x/y:1 -> unmapped registry host other.example' "${TMP}/g-unmapped/out" \
    && pass "an image from an unmapped registry host fails the guard, by name" \
    || fail "an unmapped image did not fail the guard"
fi

sh -n "${S03}" 2>"${TMP}/syntax.err" && pass "the rendered step-03 script parses as POSIX sh" || fail "the rendered step-03 script does not parse: $(head -2 "${TMP}/syntax.err")"

echo
if [ "${FAILURES}" -gt 0 ]; then
  echo "[step7922] FAILURES=${FAILURES}"; exit 1
fi
echo "[step7922] all cases passed"
