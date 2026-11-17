#!/usr/bin/env bash
# #7938 — step 03 copies every image the kit's Flux Kustomizations (and its
# plain manifests) will run, and ends by name on one it cannot.
#
# Release delivery to hw307 (16 Nov 2026): after cutover the Job
# sovereign-object-bootstrap sat in ImagePullBackOff in both regions:
#   failed to resolve reference "ghcr.io/openova-io/openova/sovereign-ops:907fc2a": ... not found
# The Job is a plain manifest under the path of the kit's Flux Kustomization
# sovereign-object (slot 12b), and its tag comes from that Kustomization's
# `images:` entry. Step 03 built its image set from the live walk (the Job does
# not exist before promote), the chart pins and the images those charts render:
# nothing read a Kustomization's path or its `images:`, so the image was never
# copied into the Sovereign's own registry, where the node mirror sends ghcr.io.
#
# Every case EXECUTES the shell the chart renders: the prewarm-kit-images block
# (with a curl stub that serves a fixture tree the way the local Gitea serves
# the branch), the run guard and the follower-release-pins block, the seed line
# of the step. On the tree before the change the block does not exist, so A to
# E fail, and F fails because the run guard never sees the kit's image.
#
# Usage: bash tests/step7938-prewarm-kit-images.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
REPO_ROOT="$(cd "${CHART_DIR}/../../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

render() { # <extra --set> <out>: the rendered step-03 run.sh
  # shellcheck disable=SC2086
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn=sov.example.test $1 >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
    echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
  }
  python3 - "${TMP}/render.yaml" "$2" <<'PY'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "ConfigMap" and d["metadata"]["name"] == "cutover-step-03-harbor-prewarm":
        open(sys.argv[2], "w").write(d["data"]["run.sh"])
PY
  [ -s "$2" ] || { echo "FAIL: could not extract step 03 from the render"; exit 1; }
}
block() { awk -v a="$2" 'index($0, a" BEGIN") {f=1; next} index($0, a" END") {exit} f {print}' "$1"; }
render "" "${TMP}/s03.sh"
render "--set secondaryRegions.prewarmSecondaryHarbor=true" "${TMP}/s03-follower.sh"
S03="${TMP}/s03.sh"
block "${S03}" "prewarm-kit-images" >"${TMP}/kit.sh"
block "${S03}" "prewarm-run-guard" >"${TMP}/guard.sh"
block "${S03}" "prewarm-sovereign-values" >"${TMP}/values.sh"
block "${TMP}/s03-follower.sh" "follower-release-pins" >"${TMP}/follower.sh"

# ───────────────────────── the fixture tree, as the local Gitea serves the branch
BRANCH=release/20261116T120000Z-907fc2a
DIGEST="sha256:$(printf '%064d' 7)"
R="${TMP}/repo"
mkdir -p "${R}/clusters/_template/bootstrap-kit" "${R}/clusters/_template/sovereign-object/cron" \
  "${R}/clusters/_template/generated/nested" "${R}/products/fixture/crds"
cat >"${R}/clusters/_template/bootstrap-kit/12b-writer.yaml" <<'YAML'
# A comment is never a ref: image: ghcr.io/openova-io/openova/in-a-comment:1
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: sovereign-object
  namespace: flux-system
spec:
  interval: 10m
  path: ./clusters/_template/sovereign-object
  prune: true
  sourceRef:
    kind: GitRepository
    name: openova
  images:
    - name: ghcr.io/openova-io/openova/sovereign-ops
      newTag: 907fc2a
    - name: docker.io/library/busybox
      newName: quay.io/mirror/busybox
      digest: DIGEST_HERE
  patches:
    - patch: |-
        apiVersion: batch/v1
        kind: Job
        metadata:
          name: x
  postBuild:
    substitute:
      SLOT: "12b"
YAML
sed -i "s/DIGEST_HERE/${DIGEST}/" "${R}/clusters/_template/bootstrap-kit/12b-writer.yaml"
cat >"${R}/clusters/_template/bootstrap-kit/13-generated.yaml" <<'YAML'
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: generated
  namespace: flux-system
spec:
  path: ./clusters/_template/generated/
  sourceRef: {kind: GitRepository, name: openova}
  images:
  - name: ghcr.io/openova-io/openova/agent
    newTag: "4d5e6f"
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: substituted-path
  namespace: flux-system
spec:
  path: ./clusters/${CLUSTER_DIR}
  sourceRef: {kind: GitRepository, name: openova}
YAML
cat >"${R}/clusters/_template/bootstrap-kit/50-plain.yaml" <<'YAML'
apiVersion: v1
kind: Namespace
metadata: {name: plain}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: plain-writer, namespace: plain}
spec:
  template:
    spec:
      containers:
        - name: w
          image: "ghcr.io/openova-io/openova/plain-writer:1a2b3c"
---
apiVersion: batch/v1
kind: Job
metadata: {name: substituted, namespace: plain}
spec:
  template:
    spec:
      containers:
        - name: s
          image: ghcr.io/openova-io/openova/subst:${SOVEREIGN_TAG}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata: {name: bp-x, namespace: flux-system}
spec:
  values:
    image: ghcr.io/openova-io/openova/helm-value:1
    sidecar:
      containers:
        - image: ghcr.io/openova-io/openova/helm-value-too:1
YAML
printf 'apiVersion: v1\nkind: Namespace\nmetadata: {name: other}\n' >"${R}/clusters/_template/bootstrap-kit/kustomization.yaml"
cat >"${R}/clusters/_template/sovereign-object/kustomization.yaml" <<'YAML'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../../products/fixture/crds/sovereign.yaml
  - rbac.yaml
  - job.yaml   # the writer
  - ./cron
  - github.com/example/remote//deploy?ref=v1
patches:
  - patch: |-
      apiVersion: apiextensions.k8s.io/v1
      kind: CustomResourceDefinition
      metadata: {name: sovereigns.catalyst.openova.io}
YAML
printf 'apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata: {name: sovereigns.catalyst.openova.io}\n' \
  >"${R}/products/fixture/crds/sovereign.yaml"
printf 'apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: writer}\n' >"${R}/clusters/_template/sovereign-object/rbac.yaml"
cat >"${R}/clusters/_template/sovereign-object/job.yaml" <<'YAML'
apiVersion: batch/v1
kind: Job
metadata:
  name: sovereign-object-${NETWORK_REGISTRY_REVISION:=bootstrap}
spec:
  template:
    spec:
      initContainers:
        - {name: wait, image: docker.io/library/busybox:1.36}
      containers:
        - name: writer
          image: ghcr.io/openova-io/openova/sovereign-ops
YAML
printf 'resources:\n- cronjob.yaml\n' >"${R}/clusters/_template/sovereign-object/cron/kustomization.yaml"
cat >"${R}/clusters/_template/sovereign-object/cron/cronjob.yaml" <<'YAML'
apiVersion: batch/v1
kind: CronJob
metadata: {name: rewrite}
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: a
              image: ghcr.io/openova-io/openova/sovereign-ops:old
            - name: b
              image: ghcr.io/openova-io/openova/x:${TAG}
YAML
cat >"${R}/clusters/_template/sovereign-object/unlisted.yaml" <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata: {name: unlisted}
spec:
  template:
    spec:
      containers:
        - {name: u, image: ghcr.io/openova-io/openova/unlisted:1}
YAML
cat >"${R}/clusters/_template/generated/a.yaml" <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata: {name: agent}
spec:
  template:
    spec:
      containers:
        - name: agent
          image: ghcr.io/openova-io/openova/agent
YAML
cat >"${R}/clusters/_template/generated/nested/ds.yaml" <<YAML
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: agent-node}
spec:
  template:
    spec:
      containers:
        - name: agent
          image: ghcr.io/openova-io/openova/agent:old@${DIGEST}
YAML
printf 'not yaml\n' >"${R}/clusters/_template/generated/README.md"

# The curl stub: the Gitea contents and raw APIs over a tree; it refuses another
# ref than the branch, a call without the token, and every path listed in deny.
mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/curl" <<'SH'
#!/bin/sh
out=""; url=""; tok=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -H) tok="$2"; shift 2 ;;
    --retry|--retry-delay) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "${url}" >>"${STUB_LOG}"
[ "${tok}" = "Authorization: token ${STUB_PAT}" ] || exit 22
case "${url}" in
  "${STUB_API}/raw/"*) kind=raw; p="${url#"${STUB_API}/raw/"}" ;;
  "${STUB_API}/contents/"*) kind=ls; p="${url#"${STUB_API}/contents/"}" ;;
  *) exit 6 ;;
esac
[ "${p##*\?ref=}" = "${STUB_REF}" ] || exit 22
p="${p%\?ref=*}"
grep -qxF "${p}" "${STUB_DENY}" 2>/dev/null && exit 22
f="${STUB_REPO}/${p}"
if [ "${kind}" = raw ]; then [ -f "${f}" ] || exit 22; cp "${f}" "${out}"; exit 0; fi
[ -d "${f}" ] || exit 22
sep="["
for e in "${f}"/*; do
  [ -e "${e}" ] || continue
  t=file; [ -d "${e}" ] && t=dir
  printf '%s{"type":"%s","name":"%s","path":"%s"}' "${sep}" "${t}" "${e##*/}" "${p}/${e##*/}"; sep=","
done
printf ']\n'
SH
chmod 0755 "${TMP}/bin/curl"
export STUB_LOG="${TMP}/curl.log" STUB_PAT=pat-7938 STUB_API=http://gitea-http.gitea.svc:3000/api/v1/repos/openova/openova
export STUB_REF="${BRANCH}" STUB_DENY="${TMP}/deny"
cat >"${TMP}/drive.sh" <<'SH'
set -eu
bk_pat=pat-7938; bk_api="${STUB_API}"; UPSTREAM_BRANCH="${STUB_REF}"
. "$1"
prewarm_kit_images "$2" "$3" || exit 3
SH
# kit_images <dir> <repo> <kit dir> [shell]: the block over a tree; images, err and rc in <dir>.
kit_images() {
  mkdir -p "$1"
  STUB_REPO="$2" PATH="${TMP}/bin:${PATH}" ${4:-sh} "${TMP}/drive.sh" "${TMP}/kit.sh" "$3" "$1/work" >"$1/images" 2>"$1/err"
  echo $? >"$1/rc"
}
KIT="${R}/clusters/_template/bootstrap-kit"

echo "[step7938-kit] A: a Flux Kustomization's images: retags the Job under its path"
if [ ! -s "${TMP}/kit.sh" ]; then
  fail "the rendered step 03 has no prewarm-kit-images block: nothing reads what the kit's Flux Kustomizations apply"
else
  D="${TMP}/a"; : >"${TMP}/deny"; kit_images "${D}" "${R}" "${KIT}"
  [ "$(cat "${D}/rc")" = 0 ] && pass "the block reads the tree" || fail "the block failed (rc $(cat "${D}/rc")): $(tail -3 "${D}/err")"
  for want in ghcr.io/openova-io/openova/sovereign-ops:907fc2a "quay.io/mirror/busybox@${DIGEST}" ghcr.io/openova-io/openova/agent:4d5e6f; do
    grep -qxF "${want}" "${D}/images" && pass "named: ${want}" || fail "not named: ${want} (images: $(tr '\n' ' ' <"${D}/images"))"
  done
  grep -qxF ghcr.io/openova-io/openova/sovereign-ops "${D}/images" && fail "the bare ref was kept; the images: entry was not applied" || pass "the bare ref is never named as it is written"
  grep -q ':old' "${D}/images" && fail "a tag the images: entry replaces survived" || pass "newTag replaces a written tag (and the digest beside it)"
  grep -q 'unlisted' "${D}/images" && fail "a file the folder's kustomization.yaml does not list was read as a resource" || pass "only the resources the folder's kustomization.yaml lists are read"
  grep -q "kit-images: ghcr.io/openova-io/openova/sovereign-ops:907fc2a (Flux Kustomization sovereign-object, clusters/_template/sovereign-object/job.yaml)" "${D}/err" \
    && pass "the log names the Kustomization and the file" || fail "the log does not name where the ref comes from: $(grep 907fc2a "${D}/err")"
  grep -q '/products/fixture/crds/sovereign.yaml?ref=' "${TMP}/curl.log" && pass "a resource outside the path (../../../) is read from the repository root" || fail "the ../ resource was not read"
  grep -q 'clusters/_template/generated/nested?ref=' "${TMP}/curl.log" && pass "a folder without kustomization.yaml is read below, as Flux generates it" || fail "the generated folder was not walked"

  echo "[step7938-kit] B: a plain manifest of the kit is named; a HelmRelease's values are not"
  grep -qxF ghcr.io/openova-io/openova/plain-writer:1a2b3c "${D}/images" && pass "named: the kit's Deployment ghcr.io/openova-io/openova/plain-writer:1a2b3c" || fail "the kit's plain Deployment is not named"
  grep -q 'helm-value\|in-a-comment' "${D}/images" && fail "a HelmRelease value or a comment was named as a kit image" || pass "HelmRelease values and comments are not kit images"

  echo "[step7938-kit] C: a \${VAR} ref and a substituted path are skipped by name, never guessed"
  for want in 'SKIP ghcr.io/openova-io/openova/subst:${SOVEREIGN_TAG} (50-plain.yaml)' 'SKIP ghcr.io/openova-io/openova/x:${TAG} (Flux Kustomization sovereign-object' \
      "SKIP Flux Kustomization substituted-path: its path './clusters/\${CLUSTER_DIR}'"; do
    grep -qF "${want}" "${D}/err" && pass "skipped by name: ${want}" || fail "not skipped by name: ${want}"
  done
  grep -q '\$' "${D}/images" && fail "a ref holding \${...} reached the image set" || pass "no \${...} ref reaches the image set"

  echo "[step7938-kit] D: what the block does not apply is named"
  grep -qF 'sovereign-object: the patches of clusters/_template/sovereign-object is not applied' "${D}/err" && pass "the folder's patches are named as not applied" || fail "the folder's patches are not named"
  grep -qF 'Flux Kustomization sovereign-object: its spec.patches is not applied' "${D}/err" && pass "the Kustomization's spec.patches is named as not applied" || fail "spec.patches is not named"
  grep -qF 'sovereign-object: the remote resource github.com/example/remote//deploy?ref=v1 is not applied' "${D}/err" && pass "a remote resource is named, never fetched" || fail "the remote resource is not named"

  echo "[step7938-kit] E: a file of the tree that cannot be read fails the read, by name"
  D="${TMP}/e"; printf 'clusters/_template/sovereign-object/job.yaml\n' >"${TMP}/deny"; kit_images "${D}" "${R}" "${KIT}"; : >"${TMP}/deny"
  [ "$(cat "${D}/rc")" = 3 ] && grep -qF 'could not read clusters/_template/sovereign-object/job.yaml' "${D}/err" \
    && pass "the read fails and names the file" || fail "an unreadable file did not fail the read (rc $(cat "${D}/rc")): $(tail -2 "${D}/err")"
  if command -v busybox >/dev/null 2>&1; then
    # The Job runs /bin/sh and awk of busybox (alpine/k8s); run the block there too.
    mkdir -p "${TMP}/bb"; for a in awk sed grep sort cat mkdir rm dirname cp; do ln -sf "$(command -v busybox)" "${TMP}/bb/${a}"; done
    D="${TMP}/e-bb"; PATH="${TMP}/bb:${PATH}" kit_images "${D}" "${R}" "${KIT}" "busybox sh"
    cmp -s "${D}/images" "${TMP}/a/images" && pass "busybox sh and awk name the same set" || fail "busybox names another set: $(tr '\n' ' ' <"${D}/images") $(tail -2 "${D}/err")"
  fi
fi

echo "[step7938-kit] F: the step's seed hands the kit's images to the run guard, which fails by name on one it cannot copy"
D="${TMP}/f"; mkdir -p "${D}"
printf 'ghcr.io/openova-io/openova/sovereign-ops:907fc2a\nghcr.io/openova-io/openova/plain-writer:1a2b3c\n' >"${D}/kit"
printf 'ghcr.io/openova-io/openova/plain-writer:1a2b3c\nghcr.io/openova-io/openova/catalyst-api:1\n' >"${D}/present"
printf 'ghcr.io/openova-io/openova/catalyst-api:1\tprimary\n' >"${D}/run.tsv"
grep -m1 'do prewarm_release_seed' "${S03}" >"${D}/seed.sh"
( set -eu
  UPSTREAM_BRANCH="${BRANCH}"; cpdir="${D}/cp"; mkdir -p "${cpdir}"
  prewarm_list_images() { :; }
  offlinemirror_local_paths() { printf '%s\n' "${1#ghcr.io/}"; }
  harbor_durable_digest() { grep -qxF "ghcr.io/$1" "${D}/present" && echo sha256:aa; return 0; }
  copy_one() { echo "copy_one $1" >>"${D}/copies"; return 1; }
  . "${TMP}/values.sh"
  . "${TMP}/guard.sh"
  kit_img="${D}/kit"; run_images_file="${D}/run.tsv"
  . "${D}/seed.sh"
  prewarm_run_guard "${D}/rg" "${run_images_file}"
) >"${D}/out" 2>&1
rc=$?
[ "${rc}" -ne 0 ] && pass "the step fails" || fail "the run guard passed although the kit's Kustomization runs an image the registry lacks"
grep -F 'RUNNING-MISSING ghcr.io/openova-io/openova/sovereign-ops:907fc2a' "${D}/out" | grep -qF "pinned by ${BRANCH}" \
  && pass "it names the image and the branch that pins it" || fail "the guard does not name sovereign-ops:907fc2a: $(grep -E 'FATAL|MISSING|PASS' "${D}/out" | head -3)"
[ "$(grep -c 'copy_one ghcr.io/openova-io/openova/sovereign-ops:907fc2a' "${D}/copies" 2>/dev/null)" = 1 ] && pass "it tries one copy first" || fail "the guard did not try exactly one copy of the kit's image"
grep -q 'plain-writer' "${D}/copies" 2>/dev/null && fail "a kit image already in the registry was copied" || pass "a kit image already in the registry is not copied"
grep -qF 'prewarm_kit_images "${bk_dir}" /tmp/kit-images >"${kit_img}" || {' "${S03}" \
  && pass "Phase A2-pins reads the kit's images from the branch it reads the pins from, and fails the step when it cannot" \
  || fail "Phase A2-pins does not call prewarm_kit_images over the kit it fetched"

echo "[step7938-kit] G: the follower legs get the kit's openova-io images"
if [ -s "${TMP}/follower.sh" ]; then
  D="${TMP}/g"; mkdir -p "${D}"; : >"${D}/sec-charts"; : >"${D}/sec-img"
  printf 'ghcr.io/openova-io/openova/sovereign-ops:907fc2a\nquay.io/mirror/busybox:1\n' >"${D}/kit"
  ( set -eu; region=region-b; UPSTREAM_BRANCH="${BRANCH}"; sec_charts="${D}/sec-charts"; sec_img="${D}/sec-img"
    chart_installed=""; kit_img="${D}/kit"; . "${TMP}/follower.sh" ) >"${D}/out" 2>&1 || fail "the follower block failed: $(cat "${D}/out")"
  grep -qxF ghcr.io/openova-io/openova/sovereign-ops:907fc2a "${D}/sec-img" && pass "the follower's image list holds the kit's image" || fail "the follower's image list lacks the kit's image: $(tr '\n' ' ' <"${D}/sec-img")"
  grep -q quay.io "${D}/sec-img" && fail "the follower took a non-openova-io image" || pass "the follower keeps to openova-io"
else
  fail "no follower-release-pins block in the follower render"
fi

echo "[step7938-kit] H: the repository's own kit names slot 12b's writer image with its images: tag"
SLOT="${REPO_ROOT}/clusters/_template/bootstrap-kit/12b-sovereign-object.yaml"
if [ ! -f "${SLOT}" ] || [ ! -s "${TMP}/kit.sh" ]; then
  [ -s "${TMP}/kit.sh" ] && echo "  NOT MEASURED — ${SLOT} is not in this tree" || fail "no block to run over the repository's kit"
else
  tag=$(awk '/- name: ghcr\.io\/openova-io\/openova\/sovereign-ops$/ {getline; sub(/^ *newTag: */, ""); gsub(/"/, ""); print; exit}' "${SLOT}")
  D="${TMP}/h"; kit_images "${D}" "${REPO_ROOT}" "${REPO_ROOT}/clusters/_template/bootstrap-kit"
  [ "$(cat "${D}/rc")" = 0 ] && grep -qxF "ghcr.io/openova-io/openova/sovereign-ops:${tag}" "${D}/images" \
    && pass "named: ghcr.io/openova-io/openova/sovereign-ops:${tag} ($(grep -c . "${D}/images") kit image(s) in all)" \
    || fail "the repository's kit does not name sovereign-ops:${tag} (rc $(cat "${D}/rc")): $(tr '\n' ' ' <"${D}/images") $(tail -3 "${D}/err")"
fi

if [ "${FAILURES}" -ne 0 ]; then echo "[step7938-kit] ${FAILURES} failure(s)"; exit 1; fi
echo "[step7938-kit] all cases pass"
