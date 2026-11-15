#!/usr/bin/env bash
# #7900 — six defects of the hand-over chain that a fresh two-region
# installation met on 15 November 2026. Each case below fails on the tree
# before the repair: it either EXECUTES the shell the chart renders, with
# stubbed tools, or reads the render of the kit slot's own values.
#
#   D6   the chart shipped a Helm hook that waited for the hand-over, so the
#        first phase waited 900 s for it on every fresh installation.
#   D10  step 03 read a refused push to the local registry as a failure of the
#        parent's proxy and sent the whole wave to the public registries; the
#        pushes were refused because they left before CoreDNS served the
#        registry pin.
#   D11  the follower legs followed the database-pair flag, which flips after
#        step 01 has been stamped.
#   D14  step 03's image guard stopped the chain on images of a catalogue chart
#        that is installed nowhere.
#   D16  the hold denied a region's Envoy the backends in the other region, and
#        the survival window never asked the front door.
#   D17  step 11 reported success while the owning Kustomization still put the
#        old Provider package back.
#
# Usage: bash tests/fresh-install-handover-chain.sh [CHART_DIR]
# REPO_ROOT (env) overrides where clusters/ is read from.
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(cd "${CHART_DIR}/../../.." && pwd)}"
SLOT="${REPO_ROOT}/clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml"
FQDN="sov.example.test"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }
has()  { grep -qF -- "$1" "$2"; }

render() { # $1 = outfile, rest = helm arguments
  local out="$1"; shift
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" "$@" >"${out}" 2>"${TMP}/render.err" || {
    echo "helm template failed ($*):"; cat "${TMP}/render.err"; exit 1
  }
}

# extract <render> <outdir>: one file per step script (<stepName>.sh) and one
# per key of the two follower script ConfigMaps.
extract() {
  mkdir -p "$2"
  python3 - "$1" "$2" <<'PY'
import sys, yaml
render, out = sys.argv[1], sys.argv[2]
for d in yaml.safe_load_all(open(render)):
    if not d or d.get("kind") != "ConfigMap":
        continue
    data = d.get("data") or {}
    name = d["metadata"]["name"]
    step = data.get("stepName")
    if step:
        if "run.sh" in data:
            open(f"{out}/{step}.sh", "w").write(data["run.sh"])
        elif "podSpec" in data:
            spec = yaml.safe_load(data["podSpec"])
            open(f"{out}/{step}.sh", "w").write(spec["containers"][0]["args"][0])
            open(f"{out}/{step}.podspec.yaml", "w").write(data["podSpec"])
    if name in ("cutover-secondary-harbor-prewarm-script", "cutover-secondary-mirror-script"):
        for k, v in data.items():
            open(f"{out}/{name}.{k}", "w").write(v)
PY
}

# fn <script> <name>: print the shell function <name> (column-0 definition).
fn() { awk -v n="$2" '$0 ~ "^"n"\\(\\) \\{" {f=1} f {print} f && (/^\}$/ || $0 ~ "^"n"\\(\\) \\{.*\\}$") {exit}' "$1"; }

render "${TMP}/single.yaml"
render "${TMP}/two.yaml" --set secondaryRegions.replicaRegion=hw-x-b-rtz-prod
extract "${TMP}/single.yaml" "${TMP}/single"
extract "${TMP}/two.yaml" "${TMP}/two"
for f in gitea-mirror harbor-prewarm egress-block-test crossplane-provider-pivot; do
  [ -s "${TMP}/single/${f}.sh" ] || { echo "FAIL: could not extract the ${f} step script from the render"; exit 1; }
done

# ───────────────────────────────────────────────────────────────────────── D6
echo "[fresh-install] D6: nothing in the chart can make Helm wait for the hand-over"
d6_bad=0
for v in single two; do
  if grep -Eq '^[[:space:]]*"?helm\.sh/hook[-a-z]*"?:' "${TMP}/${v}.yaml"; then
    d6_bad=1
    fail "the ${v}-region render carries a helm.sh/hook annotation: Helm waits for a hook even with disableWait"
  fi
  if grep -q -e 'HANDOVER_WAIT_SECONDS' -e '/api/v1/internal/cutover/trigger' "${TMP}/${v}.yaml"; then
    d6_bad=1
    fail "the ${v}-region render still polls the cutover trigger until the hand-over"
  fi
done
[ "${d6_bad}" = 0 ] && pass "no Helm hook and no hand-over poll in the single-region and the two-region render"
if ls "${CHART_DIR}/templates" | grep -q 'auto-trigger'; then
  fail "templates/ still holds an auto-trigger template"
else
  pass "templates/ holds no auto-trigger template"
fi

# ──────────────────────────────────────────────────────────────────────── D11
echo "[fresh-install] D11: the kit slot switches the follower legs on at installation time, without the database-pair flag"
# The slot's own values, substituted the way Flux postBuild does it, for the
# variables a region carries at INSTALLATION time: SOVEREIGN_REPLICA_REGION is
# set on a two-region Sovereign, SOVEREIGN_ENABLE_CNPG_PAIR is not set yet.
slot_values() { # $1 = replica region ("" = single region), $2 = outfile
  python3 - "${SLOT}" "$1" "$2" <<'PY'
import re, sys, yaml
slot, replica, out = sys.argv[1], sys.argv[2], sys.argv[3]
env = {"SOVEREIGN_REPLICA_REGION": replica, "SOVEREIGN_FQDN": "sov.example.test"}
text = open(slot).read()
pat = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:[-=]|[-=])([^${}]*))?\}")
def sub(m):
    name, op, default = m.group(1), m.group(2), m.group(3)
    val = env.get(name)
    if op and (val is None or (op.startswith(":") and val == "")):
        return default or ""
    return val if val is not None else ""
for _ in range(8):
    new = pat.sub(sub, text)
    if new == text:
        break
    text = new
for d in yaml.safe_load_all(text):
    if d and d.get("kind") == "HelmRelease":
        yaml.safe_dump(d["spec"].get("values") or {}, open(out, "w"))
        break
else:
    sys.exit("no HelmRelease in the slot")
PY
}
if [ ! -f "${SLOT}" ]; then
  fail "the kit slot ${SLOT} is missing"
else
  slot_values "hw-x-b-rtz-prod" "${TMP}/slot-two.values.yaml" || fail "could not read the slot's values (two regions)"
  slot_values "" "${TMP}/slot-single.values.yaml" || fail "could not read the slot's values (single region)"
  render "${TMP}/slot-two.yaml" -f "${TMP}/slot-two.values.yaml"
  render "${TMP}/slot-single.yaml" -f "${TMP}/slot-single.values.yaml"
  extract "${TMP}/slot-two.yaml" "${TMP}/slot-two"
  extract "${TMP}/slot-single.yaml" "${TMP}/slot-single"
  if python3 -c 'import sys,yaml; v=yaml.safe_load(open(sys.argv[1])); s=v["secondaryRegions"]; sys.exit(0 if (s.get("mirrorToSecondaryGitea") is False and s.get("prewarmSecondaryHarbor") is False) else 1)' "${TMP}/slot-two.values.yaml"; then
    pass "precondition: at installation time the slot's two database-pair flags are false (the pair is not on yet)"
  else
    fail "precondition lost: the slot's database-pair flags are not false at installation time, so this case no longer shows the race"
  fi
  if has '#6490 secondary-region mirror leg' "${TMP}/slot-two/gitea-mirror.sh" \
     && has 'name: SECONDARY_GITEA_NAMESPACE' "${TMP}/slot-two/gitea-mirror.podspec.yaml" \
     && has 'secretName: cutover-secondary-kubeconfigs' "${TMP}/slot-two/gitea-mirror.podspec.yaml"; then
    pass "two regions, pair flag unset: step 01 is rendered WITH its follower leg, its inputs and the follower kubeconfig mount"
  else
    fail "two regions, pair flag unset: step 01 is rendered WITHOUT its follower leg — the engine stamps step 01 from this render seconds after the hand-over, so the follower region is skipped"
  fi
  if has '#6764 secondary-region Harbor prewarm leg' "${TMP}/slot-two/harbor-prewarm.sh" \
     && [ -s "${TMP}/slot-two/cutover-secondary-harbor-prewarm-script.secondary-harbor-prewarm.sh" ] \
     && [ -s "${TMP}/slot-two/cutover-secondary-mirror-script.secondary-mirror.sh" ]; then
    pass "two regions, pair flag unset: step 03's follower leg and both follower script ConfigMaps are rendered"
  else
    fail "two regions, pair flag unset: step 03's follower leg or a follower script ConfigMap is missing"
  fi
  if has 'secondary-region mirror leg' "${TMP}/slot-single/gitea-mirror.sh" \
     || has 'secondary-region Harbor prewarm leg' "${TMP}/slot-single/harbor-prewarm.sh" \
     || [ -e "${TMP}/slot-single/cutover-secondary-mirror-script.secondary-mirror.sh" ]; then
    fail "single region: a follower leg is rendered"
  else
    pass "single region (empty replica region): no follower leg is rendered"
  fi
fi

# ──────────────────────────────────────────────────────────────────────── D10
echo "[fresh-install] D10: a refused push to the local registry is not a failure of the parent's proxy"
S03="${TMP}/single/harbor-prewarm.sh"
{ fn "${S03}" rl_slug; fn "${S03}" prewarm_multiarch_flags; fn "${S03}" prewarm_skopeo_copy; fn "${S03}" copy_one; } >"${TMP}/copy.sh"
for f in rl_slug prewarm_multiarch_flags prewarm_skopeo_copy copy_one; do
  grep -q "^${f}() {" "${TMP}/copy.sh" || { fail "could not slice ${f} out of the rendered step-03 script"; }
done
# run_copy <scenario>: runs the REAL copy_one for one image that comes from the
# parent's proxy. The skopeo stub fails its FIRST copy the way <scenario> says
# and succeeds afterwards; every copy's source is recorded in order.
run_copy() {
  local dir="${TMP}/d10-$1"
  rm -rf "${dir}"; mkdir -p "${dir}/cp"
  (
    set -eu
    cpdir="${dir}/cp"
    MOTHERSHIP_HOST="proxy.parent.test"
    HOST_PROJECT_MAP="docker.io:proxy-dockerhub quay.io:proxy-quay"
    HARBOR_HOST="registry.${FQDN}"
    HARBOR_USERNAME=u; HARBOR_PASSWORD=p
    PREWARM_DEST_TLS_VERIFY=false; PREWARM_SKOPEO_TIMEOUT=1s
    PREWARM_COPY_ATTEMPTS=3; PREWARM_PROXY_COPY_ATTEMPTS=3; PREWARM_PROXY_BACKOFF_BASE_SECONDS=0
    offlinemirror_normalize_ref() { printf '%s' "$1"; }
    offlinemirror_local_paths() { printf '%s' "${1#*/}"; }
    src_creds_for() { printf ''; }
    dest_already_current() { return 1; }
    harbor_durable_digest() { printf ''; }
    sleep() { :; }
    skopeo() {
      local a src="" prev="" n
      case " $* " in *" inspect "*) return 1 ;; esac
      for a in "$@"; do src="${prev}"; prev="${a}"; done
      n=$(( $(cat "${dir}/calls" 2>/dev/null | wc -l) + 1 ))
      echo "${src#docker://}" >>"${dir}/calls"
      if [ "${n}" -eq 1 ]; then
        case "$1" in
          --command-timeout) : ;;
        esac
        case "${SCENARIO}" in
          dest)   echo 'time="x" level=fatal msg="copying image 1/2 from manifest list: writing blob: initiating layer upload to /v2/proxy-dockerhub/alpine/k8s/blobs/uploads/ in registry.sov.example.test: unauthorized: unauthorized to access repository: proxy-dockerhub/alpine/k8s, action: push: unauthorized to access repository: proxy-dockerhub/alpine/k8s, action: push"' ;;
          source) echo 'time="x" level=fatal msg="initializing source docker://proxy.parent.test/proxy-dockerhub/alpine/k8s:1.30.0: reading manifest 1.30.0 in proxy.parent.test/proxy-dockerhub/alpine/k8s: manifest unknown"' ;;
          read)   echo 'time="x" level=fatal msg="copying image: writing blob: happened during read: Get \"https://proxy.parent.test/v2/proxy-dockerhub/alpine/k8s/blobs/sha256:aa\": read tcp: connection reset by peer"' ;;
        esac
        return 1
      fi
      return 0
    }
    SCENARIO="$1"
    # shellcheck source=/dev/null
    . "${TMP}/copy.sh"
    copy_one "proxy.parent.test/proxy-dockerhub/alpine/k8s:1.30.0"
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}
run_copy dest
if [ "$(sed -n 2p "${TMP}/d10-dest/calls" 2>/dev/null)" = "proxy.parent.test/proxy-dockerhub/alpine/k8s:1.30.0" ]; then
  pass "refused push: the second copy reads the SAME source (the parent's proxy) again"
else
  fail "refused push: the second copy went to '$(sed -n 2p "${TMP}/d10-dest/calls" 2>/dev/null)' instead of the parent's proxy — a destination failure was read as a source failure"
fi
if [ -e "${TMP}/d10-dest/cp/.proxy_down" ]; then
  fail "refused push: the proxy was latched DOWN, so every later image of the wave goes to the public registries first"
else
  pass "refused push: the proxy is NOT latched down"
fi
if grep -q 'DIRECT upstream' "${TMP}/d10-dest/out"; then
  fail "refused push: the step log blames the parent's proxy: $(grep 'DIRECT upstream' "${TMP}/d10-dest/out" | head -1)"
else
  pass "refused push: the step log does not blame the parent's proxy"
fi
if ls "${TMP}/d10-dest/cp/"*.ok >/dev/null 2>&1 && [ "$(cat "${TMP}/d10-dest/rc")" = 0 ]; then
  pass "refused push: the image is copied on the retry"
else
  fail "refused push: the image was not copied on the retry: $(tail -3 "${TMP}/d10-dest/out")"
fi
grep -q 'refused the write (destination failure' "${TMP}/d10-dest/cp/"*.log 2>/dev/null \
  && pass "refused push: the per-image log names the destination as the failing side" \
  || fail "refused push: the per-image log does not name the destination as the failing side"
for sc in source read; do
  run_copy "${sc}"
  if [ "$(sed -n 2p "${TMP}/d10-${sc}/calls" 2>/dev/null)" = "docker.io/alpine/k8s:1.30.0" ] && [ -e "${TMP}/d10-${sc}/cp/.proxy_down" ]; then
    pass "control (${sc} error of the proxy): the direct upstream is tried and the proxy is latched down, as before"
  else
    fail "control (${sc} error of the proxy): the direct-upstream fallback or the latch is lost (calls: $(tr '\n' ' ' <"${TMP}/d10-${sc}/calls" 2>/dev/null))"
  fi
done

echo "[fresh-install] D10: step 03 does not push before its own pod resolves the registry pin"
# run_probe <script> <pin variable assignment> — runs the REAL readiness loop.
# getent answers a public address twice, then the pinned one; curl always
# answers 401 (the public path answers 401 too, which is what hid the race).
run_probe() {
  awk '/^_pr_ok=0$/ {f=1} f && /^if \[ "\$\{_pr_ok\}" != "1" \]; then$/ {exit} f {print}' "$1" >"${TMP}/probe.sh"
  grep -q '_pr_code=' "${TMP}/probe.sh" || { echo "noslice"; return; }
  (
    set -eu
    HARBOR_HOST="registry.${FQDN}"
    eval "$2"
    : >"${TMP}/probe.n"
    curl() { printf '401'; }
    sleep() { :; }
    seq() { command seq 1 6; }
    getent() {
      echo x >>"${TMP}/probe.n"
      if [ "$(wc -l <"${TMP}/probe.n")" -le 2 ]; then echo "203.0.113.59  STREAM registry.${FQDN}"; else echo "10.96.22.235  STREAM registry.${FQDN}"; fi
    }
    # shellcheck source=/dev/null
    . "${TMP}/probe.sh" >/dev/null
    echo "${_pr_ok}:${_pr_i}"
  ) 2>&1 | tail -1
}
r=$(run_probe "${S03}" '_cip=10.96.22.235')
[ "${r}" = "1:3" ] && pass "primary: ready at the third probe, the first one whose lookup returns the pinned address (not at the first 401)" \
  || fail "primary: the readiness loop ended '${r}' (wanted ready at probe 3): a 401 over the public path counts as ready before CoreDNS serves the pin"
r=$(run_probe "${S03}" 'unset _cip')
[ "${r}" = "1:1" ] && pass "primary, pin switched off: ready at the first 401, as before" || fail "primary, pin switched off: the readiness loop ended '${r}' (wanted 1:1)"
SEC="${TMP}/two/cutover-secondary-harbor-prewarm-script.secondary-harbor-prewarm.sh"
r=$(run_probe "${SEC}" 'REGISTRY_PIN_IP=10.96.22.235')
[ "${r}" = "1:3" ] && pass "follower Job: ready only when its pod resolves the pinned address" \
  || fail "follower Job: the readiness loop ended '${r}' (wanted ready at probe 3)"
r=$(run_probe "${SEC}" 'REGISTRY_PIN_IP=')
[ "${r}" = "1:1" ] && pass "follower Job, no pin address handed over: ready at the first 401, as before" || fail "follower Job, no pin address: the readiness loop ended '${r}' (wanted 1:1)"
if has 'value: "__REGISTRY_PIN_IP__"' "${TMP}/two/cutover-secondary-harbor-prewarm-script.secondary-harbor-prewarm-job.yaml" \
   && has 's/__REGISTRY_PIN_IP__/${_sec_cip:-}/g' "${TMP}/two/harbor-prewarm.sh"; then
  pass "the primary leg hands the follower Job the address it pinned"
else
  fail "the follower Job never receives the pinned address (placeholder or its substitution is missing)"
fi

# ──────────────────────────────────────────────────────────────────────── D14
echo "[fresh-install] D14: an image only a catalogue chart declares does not stop the chain"
awk '/^  cat_render_fail=0$/ {f=1} f {print} f && /^  done < "\$\{chart_hard\}"$/ {exit}' "${S03}" >"${TMP}/enum.sh"
awk '/^  img_guard_missing=\/tmp\/\.chart-image-missing\.txt$/ {f=1} f && /^else$/ {exit} f {print}' "${S03}" >"${TMP}/guard.sh"
grep -q 'chart_declared_images' "${TMP}/enum.sh" || fail "could not slice the chart-image enumeration out of the rendered step-03 script"
grep -q 'Phase A3-guard' "${TMP}/guard.sh" || fail "could not slice the A3 guard out of the rendered step-03 script"
# run_guard <missing image>: bp-app 1.0.0 is installed, bp-litmus 2.0.0 is a
# catalogue entry installed nowhere. Both declare docker.io/shared/x:1.
run_guard() {
  local dir="${TMP}/d14-$1"
  rm -rf "${dir}"; mkdir -p "${dir}/tmp"
  sed "s#/tmp/#${dir}/tmp/#g" "${TMP}/enum.sh" >"${dir}/enum.sh"
  sed "s#/tmp/#${dir}/tmp/#g" "${TMP}/guard.sh" >"${dir}/guard.sh"
  (
    set -eu
    MISSING="$2"
    chart_installed="${dir}/tmp/.chart-installed.tsv"
    chart_hard="${dir}/tmp/.chart-hard.tsv"
    printf 'bp-app\t1.0.0\n' >"${chart_installed}"
    printf 'bp-app\t1.0.0\nbp-litmus\t2.0.0\n' >"${chart_hard}"
    cat_img_set="${dir}/tmp/.chart-images.txt"; : >"${cat_img_set}"
    cat_img_installed="${dir}/tmp/.chart-images-installed.txt"; : >"${cat_img_installed}"
    cat_pull_dir="${dir}/tmp/pull"; mkdir -p "${cat_pull_dir}"
    cat_cpdir="${dir}/tmp/cp"; mkdir -p "${cat_cpdir}"
    cat_img_fail=0; cat_img_unmapped=""
    PREWARM_SKOPEO_TIMEOUT=1s; PREWARM_DEST_TLS_VERIFY=false; PREWARM_CHART_IMAGES_FATAL=true
    HARBOR_USERNAME=u; HARBOR_PASSWORD=p; HARBOR_HOST="registry.${FQDN}"
    skopeo() { local a d=""; for a in "$@"; do d="${a}"; done; mkdir -p "${d#dir:}"; : >"${d#dir:}/blob"; }
    tar() { return 0; }
    chart_declared_images() {
      case "$1" in
        *bp-app_1.0.0*)    printf 'docker.io/app/api:1\ndocker.io/shared/x:1\n' ;;
        *bp-litmus_2.0.0*) printf 'docker.io/litmus/portal:3\ndocker.io/shared/x:1\n' ;;
      esac
    }
    offlinemirror_local_paths() { printf 'proxy-dockerhub/%s' "${1#docker.io/}"; }
    harbor_durable_digest() { case "$1" in "proxy-dockerhub/${MISSING#docker.io/}") printf '' ;; *) printf 'sha256:0' ;; esac; }
    # shellcheck source=/dev/null
    . "${dir}/enum.sh"
    sort -u "${cat_img_set}" -o "${cat_img_set}"
    # shellcheck source=/dev/null
    . "${dir}/guard.sh"
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}
run_guard litmus docker.io/litmus/portal:3
if [ "$(cat "${TMP}/d14-litmus/rc")" = 0 ]; then
  pass "an image of the catalogue-only chart is missing: the guard does not stop the step"
else
  fail "an image of the catalogue-only chart is missing: the guard stopped the step (exit $(cat "${TMP}/d14-litmus/rc")): $(grep -m1 -e FATAL -e 'unbound' "${TMP}/d14-litmus/out" | cut -c1-200)"
fi
grep -q 'CATALOGUE-ONLY-MISSING docker.io/litmus/portal:3 -> proxy-dockerhub/litmus/portal:3' "${TMP}/d14-litmus/out" \
  && pass "the missing catalogue-only image is named in a warning" \
  || fail "the missing catalogue-only image is not named in the step log"
run_guard app docker.io/app/api:1
if [ "$(cat "${TMP}/d14-app/rc")" = 1 ] && grep -q 'FATAL (#5442 #5525)' "${TMP}/d14-app/out" && grep -q 'MISSING docker.io/app/api:1' "${TMP}/d14-app/out"; then
  pass "control: a missing image of an INSTALLED chart still stops the step and is named"
else
  fail "control: a missing image of an installed chart no longer stops the step (exit $(cat "${TMP}/d14-app/rc"))"
fi
run_guard shared docker.io/shared/x:1
if [ "$(cat "${TMP}/d14-shared/rc")" = 1 ] && grep -q '  MISSING docker.io/shared/x:1' "${TMP}/d14-shared/out"; then
  pass "control: an image that an installed chart AND a catalogue chart declare counts as installed"
else
  fail "control: a missing image that an installed chart also declares did not stop the step (exit $(cat "${TMP}/d14-shared/rc"))"
fi

# ──────────────────────────────────────────────────────────────────────── D16
echo "[fresh-install] D16: the hold lets in-mesh traffic through in every region"
S08="${TMP}/two/egress-block-test.sh"
# The default-deny manifest writer and the secondary extension, as rendered.
awk '/^    \{$/ && !d {f=1} f {print} f && /^    \} >\/work\/egress-deny\.yaml$/ {d=1; exit}' "${S08}" >"${TMP}/hold.sh"
awk '/^  cp \/work\/egress-deny\.yaml \/work\/egress-deny-secondary\.yaml$/ {f=1} f {print} f && /^  fi$/ {exit}' "${S08}" >"${TMP}/hold-sec.sh"
grep -q 'kind: CiliumClusterwideNetworkPolicy' "${TMP}/hold.sh" || fail "could not slice the hold manifest writer out of the rendered step-08 script"
grep -q 'egress-deny-secondary.yaml' "${TMP}/hold-sec.sh" || fail "could not slice the secondary hold extension out of the rendered step-08 script"
mkdir -p "${TMP}/work"
(
  set -eu
  ALLOW_CIDRS="10.42.0.0/16 10.96.0.0/12"; PROVIDER_API_CIDRS=""; SECONDARY_EXTRA_ALLOW_CIDRS=""
  DEFAULT_DENY_EGRESS=true; SOVEREIGN_FQDN="${FQDN}"; LOCAL_REGISTRY_HOST="registry.${FQDN}"
  FRONT_DOOR_HOSTS="console"
  getent() { case "$2" in "console.${FQDN}") echo "203.0.113.3 STREAM x" ;; "registry.${FQDN}"|"gitea.${FQDN}") echo "203.0.113.59 STREAM x" ;; esac; }
  # shellcheck source=/dev/null
  . <(sed "s#/work/#${TMP}/work/#g" "${TMP}/hold.sh")
  # shellcheck source=/dev/null
  . <(sed "s#/work/#${TMP}/work/#g" "${TMP}/hold-sec.sh")
) >"${TMP}/hold.out" 2>&1 || fail "the hold manifest writer failed: $(tail -3 "${TMP}/hold.out")"
for m in egress-deny egress-deny-secondary; do
  verdict=$(python3 - "${TMP}/work/${m}.yaml" <<'PY' 2>&1
import sys, yaml
try:
    d = yaml.safe_load(open(sys.argv[1]))
    rules = d["spec"]["egress"]
except Exception as e:
    print(f"unreadable: {e}"); sys.exit(0)
problems = []
mesh = False
for r in rules:
    for sel in r.get("toEndpoints", []) or []:
        if not sel:
            problems.append("toEndpoints [{}] selects the LOCAL cluster only")
        for e in (sel or {}).get("matchExpressions", []) or []:
            if e.get("key") == "io.cilium.k8s.policy.cluster" and e.get("operator") == "Exists" and not (sel or {}).get("matchLabels"):
                mesh = True
if not mesh:
    problems.append("no rule selects the endpoints of every cluster of the mesh (io.cilium.k8s.policy.cluster Exists)")
door = [r for r in rules if "203.0.113.3/32" in (r.get("toCIDR") or [])]
if not door:
    problems.append("the front door's own address is not opened, so the window cannot sample it")
for r in door:
    ports = [(p.get("port"), p.get("protocol")) for tp in (r.get("toPorts") or []) for p in tp.get("ports", [])]
    if ports != [("443", "TCP")]:
        problems.append(f"the front door is opened on {ports}, not on 443/TCP only")
print("; ".join(problems) if problems else "ok")
PY
)
  [ "${verdict}" = "ok" ] && pass "${m}.yaml: in-mesh endpoints of every cluster are allowed, no local-only selector, the front door is open on 443 only" \
    || fail "${m}.yaml: ${verdict}"
done

echo "[fresh-install] D16: the survival window asks the front door"
awk '/# front-door-sample BEGIN/ {f=1} f {print} /# front-door-sample END/ {exit}' "${S08}" >"${TMP}/door-fn.sh"
awk '/^FRONT_DOOR_JUDGED=""$/ {f=1} f {print} f && /^fi$/ {exit}' "${S08}" >"${TMP}/door-base.sh"
awk '/^  if \[ -n "\$\{FRONT_DOOR_JUDGED\}" \]; then$/ {f=1} f {print} f && /^  fi$/ {exit}' "${S08}" >"${TMP}/door-window.sh"
if ! grep -q 'front_door_bad()' "${TMP}/door-fn.sh" || ! grep -q 'fd_base=' "${TMP}/door-base.sh" || ! grep -q 'new_failures=' "${TMP}/door-window.sh"; then
  fail "the rendered step-08 script has no front-door sample (function, pre-hold baseline or window check missing)"
else
  # run_door <codes before the hold> <codes in the window>: one code per request.
  run_door() {
    (
      set -eu
      FRONT_DOOR_HOSTS="${3-console}"; FRONT_DOOR_SAMPLES=4; SOVEREIGN_FQDN="${FQDN}"
      echo "$1" | tr ' ' '\n' >"${TMP}/door.codes"
      curl() { local c; c=$(sed -n 1p "${TMP}/door.codes"); sed -i 1d "${TMP}/door.codes"; printf '%s' "${c}"; }
      start=$(date +%s); new_failures=0
      # shellcheck source=/dev/null
      . "${TMP}/door-fn.sh"
      # shellcheck source=/dev/null
      . "${TMP}/door-base.sh" >/dev/null
      echo "$2" | tr ' ' '\n' >"${TMP}/door.codes"
      # shellcheck source=/dev/null
      . "${TMP}/door-window.sh" >"${TMP}/door.out"
      echo "${FRONT_DOOR_JUDGED:-no}:${new_failures}"
    ) 2>&1 | tail -1
  }
  r=$(run_door "200 200 200 200" "200 403 200 403")
  if [ "${r}" = "yes:1" ] && grep -q 'console=403 console=403' "${TMP}/door.out"; then
    pass "clean before the hold, half of the requests 403 in the window: counted as a regression, with the answers named"
  else
    fail "clean before the hold, half 403 in the window: verdict '${r}' (wanted yes:1)"
  fi
  r=$(run_door "200 302 200 200" "200 200 302 200")
  [ "${r}" = "yes:0" ] && pass "clean before and in the window (2xx and 3xx): no regression" || fail "clean door: verdict '${r}' (wanted yes:0)"
  r=$(run_door "200 000 200 200" "403 403 403 403")
  [ "${r}" = "no:0" ] && pass "not clean before the hold: the door is not judged, and the step says so instead of passing it" || fail "unclean baseline: verdict '${r}' (wanted no:0)"
  r=$(run_door "200 200 200 200" "200 000 200 200")
  [ "${r}" = "yes:1" ] && pass "a request without an answer in the window counts as a regression" || fail "unanswered request in the window: verdict '${r}' (wanted yes:1)"
  r=$(run_door "" "" "")
  [ "${r}" = "no:0" ] && pass "no host configured: not judged" || fail "no host configured: verdict '${r}' (wanted no:0)"
fi
if [ "$(grep -c 'front-door-sample BEGIN' "${TMP}/single/egress-block-test.sh")" = 1 ] && has 'name: FRONT_DOOR_HOSTS' "${TMP}/single/egress-block-test.podspec.yaml"; then
  pass "the sample is part of the step in a single-region render too"
else
  fail "the single-region render has no front-door sample"
fi

# ──────────────────────────────────────────────────────────────────────── D17
echo "[fresh-install] D17: step 11 does not report success before the live Provider is pivoted for good"
S11="${TMP}/single/crossplane-provider-pivot.sh"
awk '/# live-provider-pivot BEGIN/ {f=1} f {print} /# live-provider-pivot END/ {exit}' "${S11}" >"${TMP}/wait.sh"
if ! grep -q 'wait_live_provider_pivot()' "${TMP}/wait.sh"; then
  fail "the rendered step-11 script has no wait for the live Provider (it reports success right after its Git push)"
else
  LOCAL="harbor.${FQDN}/proxy-xpkg/upbound/provider-opentofu@sha256:aa"
  UP="xpkg.upbound.io/upbound/provider-opentofu:v1.1.3"
  # run_wait <budget seconds> <one line per poll: "<package> <revision the Kustomization applied>">
  # The pivot commit is "new"; "newer" is a later commit that holds it; "old" does not.
  run_wait() {
    printf '%s\n' "$2" >"${TMP}/wait.polls"
    (
      set -eu
      LIVE_SETTLE_SECONDS="$1"; upstream_prefix="xpkg.upbound.io"; MOTHERSHIP_PROXY_PREFIX="proxy.parent.test/proxy-xpkg"
      matches="clusters/_template/infrastructure/providers/base/provider-opentofu.yaml"
      : >"${TMP}/wait.n"
      cur() { local l; l=$(sed -n "$(wc -l <"${TMP}/wait.n")p" "${TMP}/wait.polls"); [ -n "${l}" ] || l=$(tail -1 "${TMP}/wait.polls"); printf '%s' "${l}"; }
      git() {
        case "$1 ${2-}" in
          "rev-parse HEAD") echo new ;;
          "fetch "*) echo x >>"${TMP}/wait.n" ;;
          "merge-base --is-ancestor") [ "$3" = new ] && { [ "$4" = new ] || [ "$4" = newer ]; } ;;
        esac
      }
      kubectl() {
        local line; line=$(cur)
        case " $* " in
          *" providers.pkg.crossplane.io "*) printf 'provider-opentofu|%s|%s|%s\n' "${line%% *}" "${WAIT_KNS-flux-system}" "${WAIT_KS-infrastructure-providers}" ;;
          *" kustomizations.kustomize.toolkit.fluxcd.io "*) printf 'main@sha1:%s' "${line##* }" ;;
        esac
      }
      sleep() { :; }
      # shellcheck source=/dev/null
      . "${TMP}/wait.sh"
      rc=0; wait_live_provider_pivot >"${TMP}/wait.out" 2>&1 || rc=$?
      echo "${rc}:$(wc -l <"${TMP}/wait.n")"
    ) 2>&1 | tail -1
  }
  r=$(run_wait 600 "${LOCAL} old
${UP} old
${LOCAL} new")
  [ "${r}" = "0:3" ] && pass "the race of the run: patched live but the Kustomization is on the old revision (poll 1), package put back (poll 2), pivot applied (poll 3) — success only at poll 3" \
    || fail "the race of the run: ended '${r}' (wanted success at poll 3): $(tail -1 "${TMP}/wait.out" 2>/dev/null | cut -c1-200)"
  r=$(run_wait 600 "${LOCAL} newer")
  [ "${r}" = "0:1" ] && pass "the Kustomization applied a later commit that holds the pivot: success at once" || fail "later commit applied: ended '${r}' (wanted 0:1)"
  r=$(run_wait 0 "${UP} old")
  if [ "${r}" = "1:1" ] && grep -q 'FATAL' "${TMP}/wait.out" && grep -q "provider-opentofu\[live-package=${UP}\]" "${TMP}/wait.out"; then
    pass "never settles: the step fails at its deadline and names the Provider and the package it still reads"
  else
    fail "never settles: ended '${r}' (wanted failure 1:1 with the Provider named): $(tail -1 "${TMP}/wait.out" 2>/dev/null | cut -c1-200)"
  fi
  r=$(run_wait 0 "${LOCAL} old")
  if [ "${r}" = "1:1" ] && grep -q 'flux-system/infrastructure-providers-applied=main@sha1:old' "${TMP}/wait.out"; then
    pass "live package pivoted but the owning Kustomization has not applied the pivot commit: not a success (it would put the old package back)"
  else
    fail "Kustomization on the old revision: ended '${r}' (wanted failure 1:1 naming the Kustomization)"
  fi
  r=$(WAIT_KS="" WAIT_KNS="" run_wait 0 "${LOCAL} old")
  [ "${r}" = "0:1" ] && pass "a Provider no Kustomization owns: the live package alone decides" || fail "Provider without a Flux owner: ended '${r}' (wanted 0:1)"
  # Nothing between the Git phase and the wait may leave the script early.
  awk '/sed edited \$\{edited\} files/ {f=1} f {print}' "${S11}" >"${TMP}/s11-tail.sh"
  if grep -Eq '^[[:space:]]*exit 0' "${TMP}/s11-tail.sh"; then
    fail "step 11 can still exit 0 after its Git phase without waiting for the live Provider"
  elif [ "$(grep -c '^wait_live_provider_pivot$' "${TMP}/s11-tail.sh")" = 1 ] \
       && [ "$(grep -n '^wait_live_provider_pivot$' "${TMP}/s11-tail.sh" | cut -d: -f1)" -lt "$(grep -n 'step complete' "${TMP}/s11-tail.sh" | cut -d: -f1)" ]; then
    pass "every path through the Git phase runs the wait before 'step complete'"
  else
    fail "the wait is not called exactly once before 'step complete'"
  fi
  settle=$(sed -n '/name: LIVE_SETTLE_SECONDS/{n;s/.*value: "\([0-9]*\)".*/\1/p}' "${TMP}/single/crossplane-provider-pivot.podspec.yaml")
  deadline=$(sed -n 's/^activeDeadlineSeconds: \([0-9]*\)$/\1/p' "${TMP}/single/crossplane-provider-pivot.podspec.yaml")
  if [ -n "${settle}" ] && [ -n "${deadline}" ] && [ "${settle}" -lt "${deadline}" ]; then
    pass "the wait's budget (${settle}s) lies inside the step's deadline (${deadline}s)"
  else
    fail "the wait's budget ('${settle}') is not below the step's deadline ('${deadline}')"
  fi
fi

for f in "${TMP}/single"/*.sh "${TMP}/two"/*.sh; do
  sh -n "${f}" 2>"${TMP}/syntax.err" || fail "$(basename "${f}") does not parse as POSIX sh: $(head -2 "${TMP}/syntax.err")"
done

echo
if [ "${FAILURES}" -gt 0 ]; then
  echo "[fresh-install] FAILURES=${FAILURES}"; exit 1
fi
echo "[fresh-install] all cases passed"
