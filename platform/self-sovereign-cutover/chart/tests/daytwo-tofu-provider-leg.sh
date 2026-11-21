#!/usr/bin/env bash
# SOV-343 — the day-two reconciler's OpenTofu provider leg copies a listed package the
# Sovereign's registry lacks, by its digest, and leaves one it holds.
#
# WHY. After the cutover the provider pod installs every provider of the cloud
# Workspaces from the Sovereign's registry (step 11). A package listed later (a release
# that bumps a provider) is in no registry the Sovereign may use until something copies
# it; step 03 ran once.
#
# THE RULE. reconcile_tofu_once reads the list ConfigMap (tofuProviders); a version the
# registry does not serve at its tag is copied from <origin>/<namespace>/<type>@<digest>
# to <registry>/openova-io/tofu-providers/<namespace>/<type>:<version> through
# offlinemirror_copy, behind the delivery gate of the image leg (here mode=warm);
# a version the registry serves at the listed digest is left.
#
# WHAT THIS TEST DOES. It renders the reconciler and the 03a resolver, cuts the script
# before its endless loop, and runs reconcile_tofu_once with stubbed kubectl (the list),
# curl (the registry's v2 HEAD answers) and skopeo (the copy).
#   A  one listed package absent, one present: the absent one is copied by digest.
#   B  both present: nothing is copied.
# On origin/main before this change the render has no reconcile_tofu_once: the test fails.
#
# Usage: bash tests/daytwo-tofu-provider-leg.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="hw300.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template dt "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --set sovereign.harborPublicURL="https://registry.${FQDN}" \
  --show-only templates/12-daytwo-harbor-pin-reconciler.yaml --show-only templates/03a-offline-mirror-resolver.yaml \
  >"${TMP}/render.yaml" 2>"${TMP}/render.err" || { echo "helm template failed:"; cat "${TMP}/render.err"; exit 1; }
yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].args[0]' "${TMP}/render.yaml" >"${TMP}/raw.sh"
yq 'select(.kind == "ConfigMap") | .data["resolver.sh"]' "${TMP}/render.yaml" >"${TMP}/resolver.sh"
yq 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(has("value")) | .name + "=" + (.value | tostring)' \
  "${TMP}/render.yaml" >"${TMP}/env.txt"
# Cut before the endless loop; the mount paths and the work folder go to this test's folder.
awk '/^[[:space:]]*# The Day-2 loop\./{exit} {print}' "${TMP}/raw.sh" \
  | sed -e "s#/offline-mirror/resolver.sh#${TMP}/resolver.sh#g" -e "s#/tmp/daytwo-work#${TMP}/work#g" >"${TMP}/script.sh"
grep -q '^reconcile_tofu_once() {$' "${TMP}/script.sh" || { echo "[daytwo-tofu] the render of the reconciler has no reconcile_tofu_once: nothing to run"; exit 1; }
grep -q '^offlinemirror_copy() {$' "${TMP}/resolver.sh" || { echo "[daytwo-tofu] the 03a resolver has no offlinemirror_copy"; exit 1; }
grep -q '^TOFU_ORIGIN_PREFIX=ghcr.io/openova-io/tofu-providers$' "${TMP}/env.txt" || { echo "[daytwo-tofu] the reconciler does not set TOFU_ORIGIN_PREFIX"; exit 1; }

# The stubs, as commands (the script runs in its own sh). kubectl answers the list read
# from ${S}/list and nothing else; curl answers a v2 HEAD from ${S}/reg/<repo>@<ref>;
# skopeo records its arguments, answers inspect with an index of one manifest (an ordinary one:
# a package in the publisher's layout, top-level artifactType, takes offlinemirror_put_list, which
# tests/hermetic/provider-package-copy runs against real registries with the real skopeo), and
# stores what a copy wrote.
mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/kubectl" <<'K'
#!/bin/sh
case "$*" in
  *"get configmap opentofu-providers "*) printf 'opentofu-providers\n'; cat "${S}/list" ;;
  *"get configmap self-sovereign-cutover-daytwo-request"*) exit 1 ;;
  *) exit 0 ;;
esac
K
cat >"${TMP}/bin/curl" <<'C'
#!/bin/sh
u=""; for a in "$@"; do case "${a}" in http*) u="${a}" ;; esac; done
r="${u#*/v2/}"; k=$(printf '%s' "${r%/manifests/*}@${r##*/manifests/}" | tr '/:@' '___')
if [ -f "${S}/reg/${k}" ]; then printf 'HTTP/1.1 200 OK\r\nDocker-Content-Digest: %s\r\n\r\n' "$(cat "${S}/reg/${k}")"
else printf 'HTTP/1.1 404 Not Found\r\n\r\n'; fi
C
cat >"${TMP}/bin/skopeo" <<'SK'
#!/bin/sh
echo "skopeo $*" >>"${S}/skopeo.log"
case " $* " in *" inspect "*) printf '{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:%s"}]}' "$(printf 'c%.0s' $(seq 64))"; exit 0 ;; esac
for a in "$@"; do s="${d:-}"; d="${a}"; done
s="${s#docker://}"; d="${d#docker://}"; p="${d#*/}"
case "${p}" in
  *@*) printf '%s' "${p##*@}" >"${S}/reg/$(printf '%s' "${p}" | tr '/:@' '___')" ;;
  *) printf '%s' "${s##*@}" >"${S}/reg/$(printf '%s' "${p%:*}@${p##*:}" | tr '/:@' '___')" ;;
esac
SK
printf '#!/bin/sh\nexit 0\n' >"${TMP}/bin/sleep"
chmod +x "${TMP}/bin/"*

D1="sha256:$(printf '1%.0s' $(seq 64))"; D2="sha256:$(printf '2%.0s' $(seq 64))"
LP="openova-io/tofu-providers"
# leg <case> <present...>: the list of two packages, the registry serving the named
# local paths (<repo>@<version>) at their listed digests; reconcile_tofu_once in mode=warm.
leg() {
  local c="$1" s="${TMP}/$1"; shift
  mkdir -p "${s}/reg" "${TMP}/work"; : >"${s}/skopeo.log"
  printf 'registry.opentofu.org/hashicorp/random 3.9.1 %s\nregistry.opentofu.org/hashicorp/time 0.14.0 %s\n' "${D1}" "${D2}" >"${s}/list"
  for p in "$@"; do
    case "${p}" in *random*) dg="${D1}" ;; *) dg="${D2}" ;; esac
    printf '%s' "${dg}" >"${s}/reg/$(printf '%s' "${LP}/${p}" | tr '/:@' '___')"
  done
  {
    echo 'set +e'
    while IFS= read -r line; do printf 'export %q\n' "${line}"; done <"${TMP}/env.txt"
    echo 'export HARBOR_USERNAME=admin HARBOR_PASSWORD=pw GHCR_DOCKERCONFIG= RECONCILE_MODE=warm'
    cat "${TMP}/script.sh"
    echo 'reconcile_tofu_once; echo "RESULT rc=$? status=${TOFU_STATUS} listed=${TOFU_LISTED} present=${TOFU_PRESENT} copied=${TOFU_WARMED} missing=${TOFU_MISS}"'
  } >"${s}/harness.sh"
  S="${s}" PATH="${TMP}/bin:${PATH}" sh "${s}/harness.sh" >"${s}/out" 2>&1
}
held() { cat "${TMP}/$1/reg/$(printf '%s' "${LP}/$2" | tr '/:@' '___')" 2>/dev/null; }

echo "── Case A: a listed package absent from the registry is copied by its digest ──"
leg A "hashicorp/random@3.9.1"
if grep -qx 'RESULT rc=0 status=ok listed=2 present=1 copied=1 missing=0' "${TMP}/A/out" \
    && grep -qF " docker://ghcr.io/${LP}/hashicorp/time@${D2} docker://registry.${FQDN}/${LP}/hashicorp/time:0.14.0" "${TMP}/A/skopeo.log" \
    && grep -qF -- "--multi-arch index-only" "${TMP}/A/skopeo.log" && [ "$(held A hashicorp/time@0.14.0)" = "${D2}" ] \
    && ! grep -qF "/${LP}/hashicorp/random" "${TMP}/A/skopeo.log"; then
  pass "hashicorp/time 0.14.0 is copied from the origin by digest to registry.${FQDN}/${LP}/hashicorp/time:0.14.0; the present package is not touched"
else fail "case A"; sed 's/^/      | /' "${TMP}/A/out" "${TMP}/A/skopeo.log"; fi

echo "── Case B: a listed package the registry serves at its digest is left ──"
leg B "hashicorp/random@3.9.1" "hashicorp/time@0.14.0"
if grep -qx 'RESULT rc=0 status=ok listed=2 present=2 copied=0 missing=0' "${TMP}/B/out" && [ ! -s "${TMP}/B/skopeo.log" ]; then
  pass "both packages are counted present and skopeo is never called"
else fail "case B"; sed 's/^/      | /' "${TMP}/B/out" "${TMP}/B/skopeo.log"; fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[daytwo-tofu] ${FAILURES} check(s) FAILED"; exit 1; fi
echo "[daytwo-tofu] all checks passed"
