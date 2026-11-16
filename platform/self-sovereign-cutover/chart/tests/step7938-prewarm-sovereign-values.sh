#!/usr/bin/env bash
# #7938 — step 03 warms the images that the Sovereign's OWN values turn on, and
# copies a ref that already points at the local registry from its upstream.
#
# First release delivery to hw307 (16 Nov 2026, rc/20261116T021630Z-462365c):
# after the promote, nine Deployments of org-services in the primary region sat
# in ImagePullBackOff on their new tags (eight services-*:b19667e and
# marketplace:eccd351) while their old pods served. The umbrella renders
# org-services only with ingress.marketplace.enabled, which defaults to false
# and which that Sovereign's HelmRelease turns on; Phase A3 rendered every
# pinned chart at its DEFAULT values, so it never named those images. And the
# copy could not have healed them: the Deployments reference the local
# registry, which the resolver maps to itself, so copy_one had no upstream to
# copy from.
#
# Every case EXECUTES the shell the chart renders (the prewarm-sovereign-values
# and prewarm-run-guard blocks and copy_one, sliced from the rendered step-03
# script; the resolver and chart_declared_images from their own rendered
# ConfigMaps), with stubbed kubectl, skopeo and registry reads. On the tree
# before the change case A fails (the default render misses the image and the
# block does not exist), case B fails (the copy reads the local registry), and
# case C fails (the run guard never sees what the release pins).
#
# Usage: bash tests/step7938-prewarm-sovereign-values.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="sov.example.test"
LOCAL="registry.${FQDN}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}
python3 - "${TMP}/render.yaml" "${TMP}" <<'PY'
import sys, yaml
want = {"cutover-step-03-harbor-prewarm": ("run.sh", "s03.sh"), "cutover-offline-mirror-resolver": ("resolver.sh", "resolver.sh"),
        "cutover-catalog-chart-set": ("catalog-set.sh", "catalog-set.sh")}
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "ConfigMap" and d["metadata"]["name"] in want:
        key, out = want[d["metadata"]["name"]]
        open(sys.argv[2] + "/" + out, "w").write(d["data"][key])
PY
for f in s03.sh resolver.sh catalog-set.sh; do
  [ -s "${TMP}/${f}" ] || { echo "FAIL: could not extract ${f} from the render"; exit 1; }
done
S03="${TMP}/s03.sh"
fn() { awk -v n="$2" '$0 ~ "^"n"\\(\\) \\{" {f=1} f {print} f && (/^\}$/ || $0 ~ "^"n"\\(\\) \\{.*\\}$") {exit}' "$1"; }
block() { awk -v a="$2" 'index($0, a" BEGIN") {f=1; next} index($0, a" END") {exit} f {print}' "$1"; }
block "${S03}" "prewarm-sovereign-values" >"${TMP}/values.sh"
export LOCAL_REGISTRY_HOST="${LOCAL}" EXCLUDED_HOSTS="" EXCLUDED_SUBSTRINGS=""
export HOST_PROJECT_MAP="ghcr.io:proxy-ghcr docker.io:proxy-dockerhub quay.io:proxy-quay"

# ───────────────────────── the umbrella's shape: an image behind a value
FIX="${TMP}/fixture"; mkdir -p "${FIX}/templates"
cat >"${FIX}/Chart.yaml" <<'YAML'
apiVersion: v2
name: bp-fixture-platform
version: 1.4.2052
YAML
cat >"${FIX}/values.yaml" <<'YAML'
global:
  imageRegistry: ""
ingress:
  marketplace:
    enabled: false
services:
  enabled: false
extra:
  enabled: false
follower:
  enabled: false
YAML
cat >"${FIX}/templates/deploy.yaml" <<'YAML'
{{- $reg := .Values.global.imageRegistry | default "ghcr.io" }}
apiVersion: apps/v1
kind: Deployment
metadata: {name: catalyst-api}
spec:
  template:
    spec:
      containers:
        - name: api
          image: {{ $reg }}/openova-io/openova/catalyst-api:907fc2a
{{- if .Values.ingress.marketplace.enabled }}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: marketplace, namespace: org-services}
spec:
  template:
    spec:
      containers:
        - name: marketplace
          image: {{ $reg }}/openova-io/openova/marketplace:eccd351
{{- end }}
{{- if .Values.services.enabled }}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: services-auth, namespace: org-services}
spec:
  template:
    spec:
      containers:
        - name: auth
          image: {{ $reg }}/openova-io/openova/services-auth:b19667e
{{- end }}
{{- if .Values.follower.enabled }}
---
apiVersion: v1
kind: Pod
metadata: {name: follower-only}
spec:
  containers:
    - name: f
      image: ghcr.io/openova-io/openova/only-the-follower-turns-me-on:1
{{- end }}
{{- if .Values.extra.enabled }}
---
apiVersion: v1
kind: Pod
metadata: {name: extra}
spec:
  containers:
    - name: extra
      image: ghcr.io/openova-io/openova/only-a-suspended-release-turns-me-on:1
{{- end }}
YAML
helm package "${FIX}" -d "${TMP}" >/dev/null || { echo "helm package failed"; exit 1; }
TGZ="${TMP}/bp-fixture-platform-1.4.2052.tgz"

# The kubectl stub: the HelmReleases of the primary region and of one follower,
# and the ConfigMap one of them reads its values from.
mkdir -p "${TMP}/bin" "${TMP}/kc"
printf 'kind: Config\n' >"${TMP}/kc/region-b.yaml"
cat >"${TMP}/hr-primary.json" <<JSON
{"items": [
  {"metadata": {"name": "bp-catalyst-platform", "namespace": "flux-system"},
   "spec": {"chart": {"spec": {"chart": "bp-fixture-platform", "version": "1.4.2052"}},
            "values": {"global": {"imageRegistry": "${LOCAL}"}, "ingress": {"marketplace": {"enabled": true}}},
            "valuesFrom": [{"kind": "ConfigMap", "name": "platform-overlay", "valuesKey": "values.yaml"},
                           {"kind": "ConfigMap", "name": "absent-and-optional", "optional": true}]}},
  {"metadata": {"name": "unrelated", "namespace": "flux-system"},
   "spec": {"chart": {"spec": {"chart": "bp-other"}}, "values": {"extra": {"enabled": true}}}},
  {"metadata": {"name": "by-reference", "namespace": "flux-system"}, "spec": {"chartRef": {"kind": "OCIRepository", "name": "x"}}}
]}
JSON
cat >"${TMP}/hr-region-b.json" <<'JSON'
{"items": [
  {"metadata": {"name": "bp-catalyst-platform", "namespace": "flux-system"},
   "spec": {"suspend": true, "chart": {"spec": {"chart": "bp-fixture-platform"}}, "values": {"extra": {"enabled": true}}}},
  {"metadata": {"name": "bp-follower-platform", "namespace": "flux-system"},
   "spec": {"chart": {"spec": {"chart": "bp-fixture-platform"}}, "values": {"follower": {"enabled": true}}}}
]}
JSON
cat >"${TMP}/cm-platform-overlay.json" <<'JSON'
{"kind": "ConfigMap", "data": {"values.yaml": "services:\n  enabled: true\ningress:\n  marketplace:\n    enabled: false\n"}}
JSON
cat >"${TMP}/bin/kubectl" <<SH
#!/bin/sh
r=primary
[ "\$1" = "--kubeconfig" ] && { r=\$(basename "\$2" .yaml); shift 2; }
case " \$* " in
  *" get helmreleases.helm.toolkit.fluxcd.io -A -o json "*) [ -f "${TMP}/no-hr-\${r}" ] && exit 1; cat "${TMP}/hr-\${r}.json" ;;
  *" get ConfigMap platform-overlay "*) cat "${TMP}/cm-platform-overlay.json" ;;
  *) exit 1 ;;
esac
SH
chmod 0755 "${TMP}/bin/kubectl"

# values_images <dir>: prewarm_hr_values, then prewarm_chart_images for the fixture.
values_images() {
  ( set -eu
    PATH="${TMP}/bin:${PATH}"
    export CATALOG_SET_TMPDIR="$1"
    # shellcheck source=/dev/null
    . "${TMP}/resolver.sh"
    # shellcheck source=/dev/null
    . "${TMP}/catalog-set.sh"
    # The rendered block reads the follower kubeconfigs where the Job mounts them.
    sed "s#/secondary-kubeconfigs/#${TMP}/kc/#g" "${TMP}/values.sh" >"$1/values.sh"
    # shellcheck source=/dev/null
    . "$1/values.sh"
    prewarm_hr_values "$1/hr" || { echo "prewarm_hr_values failed"; exit 3; }
    prewarm_chart_images "${TGZ}" bp-fixture-platform "$1/hr" >"$1/images" 2>"$1/err"
  ) >"$1/out" 2>&1
}

echo "[step7938] A: the images the Sovereign's own values turn on are named (marketplace:eccd351, services-*:b19667e)"
D="${TMP}/a"; mkdir -p "${D}"
# shellcheck source=/dev/null
( . "${TMP}/catalog-set.sh"; CATALOG_SET_TMPDIR="${D}" chart_declared_images "${TGZ}" ) >"${D}/default" 2>/dev/null
if ! grep -q 'marketplace:eccd351' "${D}/default"; then
  pass "the default-values render (what step 03 rendered) lacks marketplace:eccd351 — the miss of 16 Nov"
else
  fail "the fixture does not reproduce the miss: the default render already names marketplace:eccd351"
fi
if [ ! -s "${TMP}/values.sh" ]; then
  fail "the rendered step 03 has no prewarm-sovereign-values block: Phase A3 renders defaults only"
else
  values_images "${D}"
  # services-auth comes from a valuesFrom ConfigMap that also says marketplace off: spec.values, merged last, wins.
  for want in ghcr.io/openova-io/openova/marketplace:eccd351 ghcr.io/openova-io/openova/services-auth:b19667e \
      ghcr.io/openova-io/openova/catalyst-api:907fc2a ghcr.io/openova-io/openova/only-the-follower-turns-me-on:1; do
    grep -qxF "${want}" "${D}/images" && pass "named: ${want}" || fail "not named: ${want} (images: $(tr '\n' ' ' <"${D}/images" 2>/dev/null); $(tail -3 "${D}/out" "${D}/err" 2>/dev/null | tr '\n' ' ')"
  done
  grep -q "^${LOCAL}/" "${D}/images" && fail "a local-registry ref was kept as it is; it must be named by its upstream" || pass "refs rendered on the local registry are named by their upstream"
  grep -q 'only-a-suspended-release' "${D}/images" && fail "a suspended release's values were rendered (it runs nothing)" || pass "a suspended release's values are not rendered"
  grep -q 'with the values of HelmRelease primary flux-system/bp-catalyst-platform' "${D}/err" && pass "the log names the HelmRelease whose values were rendered" || fail "the log does not name the HelmRelease: $(cat "${D}/err")"
  # A region whose HelmReleases cannot be read cannot be certified.
  touch "${TMP}/no-hr-region-b"; D2="${TMP}/a2"; mkdir -p "${D2}"
  values_images "${D2}"
  grep -q 'prewarm_hr_values failed' "${D2}/out" && pass "a follower whose HelmReleases cannot be read fails the enumeration" || fail "an unreadable follower was skipped silently"
  rm -f "${TMP}/no-hr-region-b"
fi
grep -q 'prewarm_hr_values /tmp/prewarm-hr-values || {' "${S03}" && grep -q 'ci_imgs=$(prewarm_chart_images "${ci_tgz}" "${ci_c}" /tmp/prewarm-hr-values' "${S03}" \
  && pass "Phase A3 reads every region's HelmReleases and renders through prewarm_chart_images" \
  || fail "Phase A3 does not render the pinned charts with the HelmReleases' values"

# ───────────────────────── B: a local ref is copied from its upstream
echo "[step7938] B: a ref that points at the local registry is copied from its upstream, never from the registry to itself"
for f in rl_slug prewarm_multiarch_flags prewarm_skopeo_copy mothership_proxy_code copy_one; do fn "${S03}" "${f}" >>"${TMP}/copy.sh"; done
grep -q '^copy_one() {' "${TMP}/copy.sh" || fail "could not slice copy_one out of the rendered step-03 script"
copy_wave() { # <dir> <ref>: the REAL copy_one with the real resolver; skopeo records its source and destination
  local dir="$1"; mkdir -p "${dir}/cp"; : >"${dir}/calls"
  ( set -eu
    cpdir="${dir}/cp"; MOTHERSHIP_HOST=""; HARBOR_HOST="${LOCAL}"; HARBOR_USERNAME=u; HARBOR_PASSWORD=p
    PREWARM_DEST_TLS_VERIFY=false; PREWARM_SKOPEO_TIMEOUT=1s; PREWARM_COPY_ATTEMPTS=1; PREWARM_PROXY_COPY_ATTEMPTS=1
    . "${TMP}/resolver.sh"
    src_creds_for() { printf ''; }
    dest_already_current() { return 1; }
    harbor_durable_digest() { grep -qxF "$1" "${dir}/present" 2>/dev/null && echo sha256:aa; return 0; }
    sleep() { :; }
    skopeo() { local a s="" p=""; case " $* " in *" inspect "*) return 1 ;; esac
      for a in "$@"; do s="${p}"; p="${a}"; done; echo "${s#docker://} -> ${p#docker://}" >>"${dir}/calls"; return 0; }
    . "${TMP}/copy.sh"
    copy_one "$2"
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}
D="${TMP}/b"; copy_wave "${D}" "${LOCAL}/openova-io/openova/services-auth:b19667e"
if grep -qxF "ghcr.io/openova-io/openova/services-auth:b19667e -> ${LOCAL}/openova-io/openova/services-auth:b19667e" "${D}/calls"; then
  pass "services-auth:b19667e is copied from ghcr.io into the path the Deployment pulls"
else
  fail "the copy read '$(head -1 "${D}/calls")' (want ghcr.io/openova-io/openova/services-auth:b19667e -> ${LOCAL}/openova-io/openova/services-auth:b19667e)"
fi
grep -q "^${LOCAL}/.* -> " "${D}/calls" && fail "a copy read the local registry as its source" || pass "no copy reads the local registry"
D="${TMP}/b2"; copy_wave "${D}" "${LOCAL}/proxy-dockerhub/library/redis:7.2"
grep -qxF "docker.io/library/redis:7.2 -> ${LOCAL}/proxy-dockerhub/library/redis:7.2" "${D}/calls" && pass "a proxy-<x> local ref is copied from its origin registry" || fail "proxy-dockerhub ref: $(cat "${D}/calls")"
D="${TMP}/b3"; mkdir -p "${D}"; printf 'openova-io/openova/catalyst-api:907fc2a\n' >"${D}/present"
copy_wave "${D}" "${LOCAL}/openova-io/openova/catalyst-api:907fc2a"
[ ! -s "${D}/calls" ] && [ "$(cat "${D}/rc")" = 0 ] && ls "${D}/cp/"*.ok >/dev/null 2>&1 \
  && pass "a local ref that is in the registry is not copied, nor read upstream" || fail "a present local ref was copied: $(cat "${D}/calls")"

# ───────────────────────── C: the run guard ends the step on what the release will run
echo "[step7938] C: the run guard also checks every image the pinned charts declare for installed releases"
block "${S03}" "prewarm-run-guard" >"${TMP}/guard.sh"
D="${TMP}/c"; mkdir -p "${D}"
printf 'ghcr.io/openova-io/openova/catalyst-api:907fc2a\n' >"${D}/present"
printf 'ghcr.io/openova-io/openova/catalyst-api:907fc2a\nghcr.io/openova-io/openova/marketplace:eccd351\n' >"${D}/installed"
printf 'ghcr.io/openova-io/openova/catalyst-api:907fc2a\tprimary\n' >"${D}/run.tsv"
( set -eu
  UPSTREAM_BRANCH=release/20261116T021630Z-462365c; cpdir="${D}/cp"; mkdir -p "${cpdir}"
  prewarm_list_images() { :; }
  offlinemirror_local_paths() { printf '%s\n' "${1#ghcr.io/}"; }
  harbor_durable_digest() { grep -qxF "ghcr.io/$1" "${D}/present" && echo sha256:aa; return 0; }
  copy_one() { echo "copy_one $1" >>"${D}/copies"; }
  if [ -s "${TMP}/values.sh" ]; then . "${TMP}/values.sh"; fi
  . "${TMP}/guard.sh"
  if command -v prewarm_release_seed >/dev/null 2>&1; then prewarm_release_seed "${D}/installed" "${D}/run.tsv"; fi
  prewarm_run_guard "${D}/rg" "${D}/run.tsv"
) >"${D}/out" 2>&1
rc=$?
[ "${rc}" -ne 0 ] && pass "the step fails" || fail "the run guard passed although the release pins an image the registry lacks"
grep -F 'RUNNING-MISSING ghcr.io/openova-io/openova/marketplace:eccd351' "${D}/out" | grep -qF 'pinned by release/20261116T021630Z-462365c' \
  && pass "it names the image and the release that pins it" || fail "the guard does not name marketplace:eccd351 and its release: $(grep -E 'FATAL|MISSING|PASS' "${D}/out" | head -3)"
grep -q 'copy_one ghcr.io/openova-io/openova/marketplace:eccd351' "${D}/copies" 2>/dev/null && pass "it tries one copy first" || fail "the guard did not try to copy the release's image"

if [ "${FAILURES}" -ne 0 ]; then echo "[step7938] ${FAILURES} failure(s)"; exit 1; fi
echo "[step7938] all cases pass"
