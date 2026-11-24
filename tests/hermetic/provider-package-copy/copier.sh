#!/bin/sh
# copier.sh - the part of provider-package-copy-test.sh that runs in the copier container (the
# image of cutover step 03), in the network namespace of the two registries: localhost:5001 (the
# origin) and localhost:5002 (the Sovereign's registry). /w holds the packages, the publisher's
# ORAS and step 03's skopeo (bin/), skopeo 1.15.2 for the control (bin/old/), the code cut from the render (cut.sh), its environment (env.sh)
# and the list (list); each case writes /w/out/case-<name>.txt, one "ok" or "FAIL" line per check.
set -u
export PATH="/w/bin:${PATH}"
OUT=/w/out
CA=/w/certs/ca.crt
ACCEPT='application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json'
PUTS=/w/out/manifest-puts.log
NOCREDS=/w/out/origin-reads-without-credential.log
. /w/env.sh

# Both registries answer /v2/ before anything starts.
for p in 5001 5002; do
  i=0
  until [ "$(curl -s -o /dev/null -w '%{http_code}' --cacert "${CA}" "https://localhost:${p}/v2/")" = 200 ]; do
    i=$((i + 1)); [ "${i}" -lt 60 ] || { echo "FAIL — the registry at localhost:${p} did not answer" >"${OUT}/case-copy.txt"; exit 1; }
    sleep 1
  done
done

# The publisher's push: oras cp --from-oci-layout of each layout to the origin, read back by digest.
while read -r layout version repo digest; do
  oras cp --from-oci-layout "${layout}:${version}" "${TOFU_ORIGIN_PREFIX}/${repo}:${version}" --to-ca-file "${CA}" --to-plain-http=false >/dev/null \
    || { echo "FAIL — oras could not push ${repo}:${version}" >"${OUT}/case-copy.txt"; exit 1; }
  have=$(oras manifest fetch --descriptor --ca-file "${CA}" --plain-http=false "${TOFU_ORIGIN_PREFIX}/${repo}:${version}" | jq -r .digest)
  [ "${have}" = "${digest}" ] || { echo "FAIL — the origin serves ${repo}:${version} at ${have}, pack built ${digest}" >"${OUT}/case-copy.txt"; exit 1; }
done </w/push
echo "pushed $(wc -l </w/push) package(s) to the origin with oras $(oras version | sed -n 's/^Version: *//p')"

# An ordinary multi-arch image list (an OCI index of a linux/amd64 and a linux/arm64 image, one
# tar+gzip layer each, no artifactType, no subject), as an image layout, pushed the same way.
O=/tmp/ppc-ordinary
mkdir -p "${O}/blobs/sha256" /tmp/ppc-layers
blob() { _h=$(sha256sum <"$1" | awk '{print $1}'); cp "$1" "${O}/blobs/sha256/${_h}"; printf 'sha256:%s %s' "${_h}" "$(wc -c <"$1" | tr -d ' ')"; }
descs=""
for a in amd64 arm64; do
  mkdir -p "/tmp/ppc-layers/${a}"; printf 'an ordinary image for %s\n' "${a}" >"/tmp/ppc-layers/${a}/hello.txt"
  tar -C "/tmp/ppc-layers/${a}" -cf "/tmp/ppc-layers/${a}.tar" hello.txt
  diff_id="sha256:$(sha256sum <"/tmp/ppc-layers/${a}.tar" | awk '{print $1}')"
  gzip -n -c "/tmp/ppc-layers/${a}.tar" >"/tmp/ppc-layers/${a}.tgz"
  set -- $(blob "/tmp/ppc-layers/${a}.tgz"); ld=$1; ls=$2
  printf '{"architecture":"%s","os":"linux","config":{},"rootfs":{"type":"layers","diff_ids":["%s"]}}' "${a}" "${diff_id}" >"/tmp/ppc-layers/${a}.cfg"
  set -- $(blob "/tmp/ppc-layers/${a}.cfg"); cd_=$1; cs=$2
  printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%s},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"%s","size":%s}]}' \
    "${cd_}" "${cs}" "${ld}" "${ls}" >"/tmp/ppc-layers/${a}.man"
  set -- $(blob "/tmp/ppc-layers/${a}.man")
  descs="${descs}${descs:+,}{\"mediaType\":\"application/vnd.oci.image.manifest.v1+json\",\"digest\":\"$1\",\"size\":$2,\"platform\":{\"architecture\":\"${a}\",\"os\":\"linux\"}}"
done
printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[%s]}' "${descs}" >/tmp/ppc-layers/index
set -- $(blob /tmp/ppc-layers/index); ORD_DIGEST=$1
printf '{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"%s","size":%s,"annotations":{"org.opencontainers.image.ref.name":"1.0"}}]}' "$1" "$2" >"${O}/index.json"
printf '{"imageLayoutVersion":"1.0.0"}' >"${O}/oci-layout"
oras cp --from-oci-layout "${O}:1.0" localhost:5001/ordinary/app:1.0 --to-ca-file "${CA}" --to-plain-http=false >/dev/null \
  || { echo "FAIL — oras could not push the ordinary list" >"${OUT}/case-ordinary.txt"; exit 1; }
echo "pushed the ordinary list localhost:5001/ordinary/app:1.0 at ${ORD_DIGEST}"

# A first-party list as docker/build-push-action pushes it (catalyst-api:4166721 on 23 Nov 2026):
# an OCI index of one linux/amd64 image and its attestation manifest (platform unknown/unknown,
# one in-toto layer), as an image layout, pushed to the first registry at openova-io/openova/app.
O=/tmp/ppc-buildx
mkdir -p "${O}/blobs/sha256" /tmp/ppc-layers/bx
printf 'a first-party image\n' >/tmp/ppc-layers/bx/hello.txt
tar -C /tmp/ppc-layers/bx -cf /tmp/ppc-layers/bx.tar hello.txt
gzip -n -c /tmp/ppc-layers/bx.tar >/tmp/ppc-layers/bx.tgz
set -- $(blob /tmp/ppc-layers/bx.tgz); ld=$1; ls=$2
printf '{"architecture":"amd64","os":"linux","config":{},"rootfs":{"type":"layers","diff_ids":["sha256:%s"]}}' "$(sha256sum </tmp/ppc-layers/bx.tar | awk '{print $1}')" >/tmp/ppc-layers/bx.cfg
set -- $(blob /tmp/ppc-layers/bx.cfg); cd_=$1; cs=$2
printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%s},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"%s","size":%s}]}' \
  "${cd_}" "${cs}" "${ld}" "${ls}" >/tmp/ppc-layers/bx.man
set -- $(blob /tmp/ppc-layers/bx.man); BX_CHILD=$1; bxs=$2
printf '{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"https://slsa.dev/provenance/v0.2","subject":[{"name":"_","digest":{"sha256":"%s"}}],"predicate":{"buildType":"https://mobyproject.org/buildkit@v1"}}' "${BX_CHILD#sha256:}" >/tmp/ppc-layers/att.json
set -- $(blob /tmp/ppc-layers/att.json); al=$1; als=$2
printf '{"architecture":"unknown","os":"unknown","config":{},"rootfs":{"type":"layers","diff_ids":["%s"]}}' "${al}" >/tmp/ppc-layers/att.cfg
set -- $(blob /tmp/ppc-layers/att.cfg); ac=$1; acs=$2
printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%s},"layers":[{"mediaType":"application/vnd.in-toto+json","digest":"%s","size":%s,"annotations":{"in-toto.io/predicate-type":"https://slsa.dev/provenance/v0.2"}}]}' \
  "${ac}" "${acs}" "${al}" "${als}" >/tmp/ppc-layers/att.man
set -- $(blob /tmp/ppc-layers/att.man)
printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"%s","size":%s,"platform":{"architecture":"amd64","os":"linux"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"%s","size":%s,"annotations":{"vnd.docker.reference.digest":"%s","vnd.docker.reference.type":"attestation-manifest"},"platform":{"architecture":"unknown","os":"unknown"}}]}' \
  "${BX_CHILD}" "${bxs}" "$1" "$2" "${BX_CHILD}" >/tmp/ppc-layers/bx.index
set -- $(blob /tmp/ppc-layers/bx.index); BX_DIGEST=$1
printf '{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"%s","size":%s,"annotations":{"org.opencontainers.image.ref.name":"1.0"}}]}' "$1" "$2" >"${O}/index.json"
printf '{"imageLayoutVersion":"1.0.0"}' >"${O}/oci-layout"
oras cp --from-oci-layout "${O}:1.0" localhost:5001/openova-io/openova/app:1.0 --to-ca-file "${CA}" --to-plain-http=false >/dev/null \
  || { echo "FAIL — oras could not push the first-party list" >"${OUT}/case-warmup.txt"; exit 1; }
echo "pushed the first-party list localhost:5001/openova-io/openova/app:1.0 at ${BX_DIGEST} (linux/amd64 child ${BX_CHILD})"
# The same list is the image the listed Blueprint and the unlisted Blueprint the kit installs pin
# in the tree of case warmup; unpublished:0.21.0 and listed-absent:1.0 are never pushed.
for r in listed-app kit-app; do
  oras cp --from-oci-layout "${O}:1.0" "localhost:5001/openova-io/openova/${r}:1.0" --to-ca-file "${CA}" --to-plain-http=false >/dev/null \
    || { echo "FAIL — oras could not push the first-party list as ${r}:1.0" >"${OUT}/case-warmup.txt"; exit 1; }
done

# The list ConfigMap is the one stub: a kubectl first on PATH answers its read from /w/list. A curl
# first on PATH records every manifest PUT (the registry-API branch) in PUTS, then runs curl; a
# skopeo first on PATH records in NOCREDS every call that reads the origin without the credential
# src_creds_for gives it (the origin host is MOTHERSHIP_HOST in env.sh), then runs skopeo.
mkdir -p /tmp/ppc-stub
cat >/tmp/ppc-stub/kubectl <<'STUB'
#!/bin/sh
case "$*" in
  *"get configmap ${TOFU_LIST_CM} "*) printf '%s\n' "${TOFU_LIST_CM}"; cat /w/list ;;
  *) echo "unexpected kubectl $*" >&2; exit 1 ;;
esac
STUB
printf '#!/bin/sh\ncase " $* " in *" -X PUT "*/manifests/*) echo "$*" >>%s ;; esac\nexec %s "$@"\n' "${PUTS}" "$(command -v curl)" >/tmp/ppc-stub/curl
printf '#!/bin/sh\ncase " $* " in *" docker://localhost:5001/"*) case " $* " in *"creds=puller:pulltok "*) ;; *) echo "$*" >>%s ;; esac ;; esac\nexec %s "$@"\n' "${NOCREDS}" "$(command -v skopeo)" >/tmp/ppc-stub/skopeo
chmod +x /tmp/ppc-stub/kubectl /tmp/ppc-stub/curl /tmp/ppc-stub/skopeo; : >"${PUTS}"; : >"${NOCREDS}"
export PATH="/tmp/ppc-stub:${PATH}"

# get REGISTRY KIND REPO REF FILE: one GET of the distribution API; prints the sha256 of the body.
get() {
  curl -fsS --cacert "${CA}" -H "Accept: ${ACCEPT}" -o "$5" "https://localhost:$1/v2/$3/$2/$4" 2>/dev/null || { : >"$5"; echo none; return 0; }
  echo "sha256:$(sha256sum <"$5" | awk '{print $1}')"
}
# same KIND SRCREPO DSTREPO REF: the document or blob is at the destination with the origin's bytes.
same() {
  s=$(get 5001 "$1" "$2" "$4" /tmp/ppc.s); d=$(get 5002 "$1" "$3" "$4" /tmp/ppc.d)
  if [ "${s}" != none ] && [ "${s}" = "${d}" ] && cmp -s /tmp/ppc.s /tmp/ppc.d; then return 0; fi
  echo "    $1 $4: origin ${s}, destination ${d}"; return 1
}
# judge LABEL SRCREPO DSTREPO TAG DIGEST: the tag serves the digest; the list, every entry and
# every blob of every entry are at the destination with the origin's digests and bytes.
judge() {
  tag=$(get 5002 manifests "$3" "$4" /tmp/ppc.t)
  ok=1; [ "${tag}" = "$5" ] || { ok=0; echo "    the tag $3:$4 serves ${tag}, want $5"; }
  same manifests "$2" "$3" "$5" || ok=0
  get 5001 manifests "$2" "$5" /tmp/ppc.i >/dev/null
  nm=0; nb=0
  for m in $(jq -r '.manifests[].digest' /tmp/ppc.i); do
    same manifests "$2" "$3" "${m}" || ok=0
    get 5001 manifests "$2" "${m}" /tmp/ppc.m >/dev/null
    for b in $(jq -r '.config.digest, .layers[].digest' /tmp/ppc.m); do same blobs "$2" "$3" "${b}" || ok=0; nb=$((nb + 1)); done
    nm=$((nm + 1))
  done
  if [ "${ok}" = 1 ] && [ "${nm}" -gt 0 ] && [ "${nb}" -gt 0 ]; then
    echo "ok   — $1: $3:$4 serves $5; the list and its ${nm} manifest(s) and ${nb} blob(s) have the origin's digests and bytes"
  else
    echo "FAIL — $1: $3:$4 is not in the destination as published"
  fi
}

echo "── case copy: Phase A5 of step 03, cut from the render, real skopeo, real registries ──"
rc=0
sh -c 'set -eu; . /w/env.sh; . "$1"; tofu_providers_copy' sh /w/cut.sh >"${OUT}/copy.out" 2>&1 || rc=$?
sed 's/^/    /' "${OUT}/copy.out"
{
  if [ "${rc}" -eq 0 ]; then echo "ok   — copy: Phase A5 exits 0"; else echo "FAIL — copy: Phase A5 exits ${rc}"; fi
  n=$(wc -l </w/list); p=$(grep -c "/v2/${TOFU_LOCAL_PATH}/.*/manifests/" "${PUTS}")
  if [ "${p}" -eq "${n}" ]; then echo "ok   — copy: each of the ${n} package index(es) was written by one registry-API manifest PUT, after its entries"
  else echo "FAIL — copy: ${p} of ${n} package index(es) took the registry-API branch"; fi
  nr=$(wc -l <"${NOCREDS}"); first=$(head -n 1 "${NOCREDS}" | cut -c1-240)
  if [ -s "${NOCREDS}" ]; then echo "FAIL — copy: ${nr} read(s) of the origin without its credential: ${first}"
  else echo "ok   — copy: every read of the origin carried the credential src_creds_for gives it"; fi
  while read -r source version digest; do
    repo="${source#registry.opentofu.org/}"
    judge copy "openova-io/tofu-providers/${repo}" "${TOFU_LOCAL_PATH}/${repo}" "${version}" "${digest}"
  done </w/list
} >"${OUT}/case-copy.txt" 2>&1

echo "── case again: a second run copies nothing ──"
rc=0
sh -c 'set -eu; . /w/env.sh; . "$1"; tofu_providers_copy' sh /w/cut.sh >"${OUT}/again.out" 2>&1 || rc=$?
sed 's/^/    /' "${OUT}/again.out"
n=$(wc -l </w/list)
if [ "${rc}" -eq 0 ] && [ "$(grep -c 'Phase A5 present ' "${OUT}/again.out")" -eq "${n}" ] && ! grep -q 'Phase A5 copy ' "${OUT}/again.out"; then
  echo "ok   — again: all ${n} package(s) present at their digests, nothing copied" >"${OUT}/case-again.txt"
else
  echo "FAIL — again: exit ${rc}; want ${n} 'present' lines and no copy" >"${OUT}/case-again.txt"
fi

echo "── case ordinary: an ordinary multi-arch image list through the same function stays on the skopeo path ──"
rc=0
sh -c 'set -eu; . /w/env.sh; . "$1"; offlinemirror_copy "localhost:5001/ordinary/app@$2" "" "localhost:5002/ordinary/app:1.0" --multi-arch all' \
  sh /w/cut.sh "${ORD_DIGEST}" >"${OUT}/ordinary.out" 2>&1 || rc=$?
sed 's/^/    /' "${OUT}/ordinary.out"
{
  if [ "${rc}" -eq 0 ]; then echo "ok   — ordinary: offlinemirror_copy exits 0"; else echo "FAIL — ordinary: offlinemirror_copy exits ${rc}"; fi
  if grep -q "/v2/ordinary/" "${PUTS}"; then echo "FAIL — ordinary: the ordinary list took the registry-API branch (a manifest PUT)"
  else echo "ok   — ordinary: the ordinary list did not take the registry-API branch (skopeo --multi-arch index-only wrote it)"; fi
  judge ordinary ordinary/app ordinary/app 1.0 "${ORD_DIGEST}"
} >"${OUT}/case-ordinary.txt" 2>&1

echo "── case order: a list written before its entries is refused by the registry ──"
read -r source version digest </w/list
repo="${source#registry.opentofu.org/}"
get 5001 manifests "openova-io/tofu-providers/${repo}" "${digest}" /tmp/ppc.o >/dev/null
code=$(curl -sS --cacert "${CA}" -u "${HARBOR_USERNAME}:${HARBOR_PASSWORD}" -X PUT -H 'Content-Type: application/vnd.oci.image.index.v1+json' \
  --data-binary @/tmp/ppc.o -o /tmp/ppc.ob -w '%{http_code}' "https://localhost:5002/v2/order/${repo}/manifests/${version}" 2>&1)
if [ "${code}" = 400 ] && grep -q MANIFEST_BLOB_UNKNOWN /tmp/ppc.ob; then
  echo "ok   — order: the index of ${repo}:${version} PUT before its entries is refused (400 MANIFEST_BLOB_UNKNOWN), so the copy wrote the entries first" >"${OUT}/case-order.txt"
else
  body=$(head -c 200 /tmp/ppc.ob)
  echo "FAIL — order: the index PUT before its entries answered ${code} ${body}: this registry does not show the order" >"${OUT}/case-order.txt"
fi

echo "── case whole: skopeo alone at step 03's version copies each package index whole, digests preserved ──"
: >"${OUT}/whole.out"
{
  while read -r source version digest; do
    repo="${source#registry.opentofu.org/}"; rc=0
    sh -c 'set -eu; . /w/env.sh; . "$1"; offlinemirror_skopeo_copy "${TOFU_ORIGIN_PREFIX}/$2@$4" "" "${HARBOR_HOST}/whole/$2:$3" --multi-arch all' \
      sh /w/cut.sh "${repo}" "${version}" "${digest}" </dev/null >>"${OUT}/whole.out" 2>&1 || rc=$?
    if [ "${rc}" -eq 0 ]; then echo "ok   — whole: $(skopeo --version) copies ${repo}:${version} in one call (--multi-arch all, --preserve-digests), exit 0"
    else echo "FAIL — whole: $(skopeo --version) copying ${repo}:${version} in one call exits ${rc}: $(grep -h 'level=fatal' "${OUT}/whole.out" | tail -n 1 | cut -c1-240)"; fi
    judge whole "openova-io/tofu-providers/${repo}" "whole/${repo}" "${version}" "${digest}"
  done </w/list
} >"${OUT}/case-whole.txt" 2>&1
sed 's/^/    /' "${OUT}/whole.out"

echo "── case control: the same whole copy with skopeo 1.15.2, the version before, is refused as measured ──"
read -r source version digest </w/list
repo="${source#registry.opentofu.org/}"
sh -c '
  set -eu; . /w/env.sh; . "$1"; export PATH="/w/bin/old:${PATH}"; skopeo --version
  offlinemirror_skopeo_copy "${TOFU_ORIGIN_PREFIX}/$2@$4" "" "${HARBOR_HOST}/control/$2:$3" --multi-arch all
' sh /w/cut.sh "${repo}" "${version}" "${digest}" >"${OUT}/control.out" 2>&1 && crc=0 || crc=$?
sed 's/^/    /' "${OUT}/control.out"
if [ "${crc}" -ne 0 ] && grep -qx 'skopeo version 1.15.2' "${OUT}/control.out" && grep -q 'Instructed to preserve digests' "${OUT}/control.out"; then
  echo "ok   — control: skopeo 1.15.2 refuses the whole copy of ${repo}:${version}, as measured on 21 Nov 2026" >"${OUT}/case-control.txt"
else
  echo "FAIL — control: skopeo 1.15.2's whole copy exited ${crc} without the measured refusal: the packages no longer have the shape that failed, and this test cannot see the defect" >"${OUT}/case-control.txt"
fi

echo "── case warmup: the warm-up's copy step, run as written, for a ref without the cut script, copies a first-party list from ghcr.io whole ──"
# A skopeo first on PATH sends every docker://ghcr.io/ reference to the first registry. The step
# runs in /w/gh, the ref being warmed (its list names app:1.0); /w/rt is the RUNNER_TEMP the
# install step wrote the copy functions to. The step sources them itself, or nothing copies.
mkdir -p /tmp/ppc-warmup-stub
cat >/tmp/ppc-warmup-stub/skopeo <<'STUB'
#!/bin/sh
for a in "$@"; do case "${a}" in docker://ghcr.io/*) a="docker://localhost:5001/${a#docker://ghcr.io/}" ;; esac; set -- "$@" "${a}"; shift; done
exec /w/bin/skopeo "$@"
STUB
chmod +x /tmp/ppc-warmup-stub/skopeo
warmup_step() { (cd "$1" && . /w/warmup-env.sh && PATH="/tmp/ppc-warmup-stub:${PATH}" bash --noprofile --norc -eo pipefail /w/warmup-step.sh); }
rc=0
warmup_step /w/gh >"${OUT}/warmup.out" 2>&1 || rc=$?
cat /tmp/warmup-copies/*.log >>"${OUT}/warmup.out" 2>/dev/null
sed 's/^/    /' "${OUT}/warmup.out"
{
  if [ "${rc}" -eq 0 ] && ls /tmp/warmup-copies/*.ok >/dev/null 2>&1; then echo "ok   — warmup: the copy step exits 0 and marks the image ok"
  else echo "FAIL — warmup: the copy step exits ${rc}, $(cat /tmp/warmup-copies/*.fail 2>/dev/null || echo 'no .ok')"; fi
  judge warmup openova-io/openova/app openova-io/openova/app 1.0 "${BX_DIGEST}"
  # The component pins of the tree: the listed Blueprint's and that of the unlisted Blueprint the
  # kit installs are copied; the unlisted Blueprint nothing installs contributes no image.
  for r in listed-app kit-app; do
    if [ -e "/tmp/warmup-copies/ghcr.io_openova-io_openova_${r}_1.0.ok" ]; then echo "ok   — warmup: the component pin ${r}:1.0 is in the list and copied"
    else echo "FAIL — warmup: the component pin ${r}:1.0 was not copied ($(ls /tmp/warmup-copies/ 2>/dev/null | tr '\n' ' ')): a listed Blueprint and one the kit installs keep their images"; fi
  done
  if grep -q 'queue ghcr.io/openova-io/openova/unpublished:' "${OUT}/warmup.out"; then echo "FAIL — warmup: the image unpublished:0.21.0 of bp-unlisted, unlisted in the catalogue seed and installed by no HelmRelease of the kit, is in the list"
  else echo "ok   — warmup: the image of bp-unlisted, unlisted in the catalogue seed and installed by no HelmRelease of the kit, is not in the list"; fi
} >"${OUT}/case-warmup.txt" 2>&1

echo "── case warmup-fail: a copy that fails fails the copy step (its list names an image ghcr does not have) ──"
frc=0
warmup_step /w/gh-fail >"${OUT}/warmup-fail.out" 2>&1 || frc=$?
sed 's/^/    /' "${OUT}/warmup-fail.out"
if [ "${frc}" -ne 0 ] && ls /tmp/warmup-copies/*absent*.fail >/dev/null 2>&1; then
  echo "ok   — warmup-fail: the copy of absent:1.0 is marked failed and the step exits ${frc}" >"${OUT}/case-warmup-fail.txt"
else
  echo "FAIL — warmup-fail: the step exits ${frc} with $(ls /tmp/warmup-copies/ 2>/dev/null | tr '\n' ' '): a copy that fails must fail the warm-up job" >"${OUT}/case-warmup-fail.txt"
fi

echo "── case warmup-listed: the missing image of a listed Blueprint still fails the copy step ──"
lrc=0
warmup_step /w/gh-listed >"${OUT}/warmup-listed.out" 2>&1 || lrc=$?
sed 's/^/    /' "${OUT}/warmup-listed.out"
{
  if [ "${lrc}" -ne 0 ] && [ -e /tmp/warmup-copies/ghcr.io_openova-io_openova_listed-absent_1.0.fail ]; then
    echo "ok   — warmup-listed: the copy of listed-absent:1.0, the pin of a listed Blueprint, is marked failed and the step exits ${lrc}"
  else echo "FAIL — warmup-listed: the step exits ${lrc} with $(ls /tmp/warmup-copies/ 2>/dev/null | tr '\n' ' '): the missing image of a listed Blueprint must fail the warm-up job"; fi
  if grep -q 'queue ghcr.io/openova-io/openova/unpublished:' "${OUT}/warmup-listed.out"; then echo "FAIL — warmup-listed: the image of the unlisted bp-unlisted is in the list"
  else echo "ok   — warmup-listed: the image of the unlisted bp-unlisted is not in the list"; fi
} >"${OUT}/case-warmup-listed.txt"

echo "── case warmup-control: skopeo at its default, the warm-up's copy before 24 Nov 2026, writes the child ──"
crc=0
/w/bin/skopeo copy --insecure-policy docker://localhost:5001/openova-io/openova/app:1.0 docker://localhost:5002/warmup-control/app:1.0 >"${OUT}/warmup-control.out" 2>&1 || crc=$?
sed 's/^/    /' "${OUT}/warmup-control.out"
tag=$(get 5002 manifests warmup-control/app 1.0 /tmp/ppc.wc)
if [ "${crc}" -eq 0 ] && [ "${tag}" = "${BX_CHILD}" ] && [ "${tag}" != "${BX_DIGEST}" ]; then
  echo "ok   — warmup-control: skopeo at its default writes the linux/amd64 child ${tag} under the tag, not the list ${BX_DIGEST}: one tag, two digests" >"${OUT}/case-warmup-control.txt"
else
  echo "FAIL — warmup-control: skopeo at its default exited ${crc} and the tag serves ${tag} (child ${BX_CHILD}, list ${BX_DIGEST}): this test cannot see the defect" >"${OUT}/case-warmup-control.txt"
fi
exit 0
