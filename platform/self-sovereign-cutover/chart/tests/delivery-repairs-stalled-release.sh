#!/usr/bin/env bash
# A delivery can repair a release that is stalled when the candidate itself
# changes that release.
#
# Measured on the standing two-region Sovereign, 25 Nov 2026: the delivery of a
# release candidate (PUT /api/v1/sovereign/release, Job
# sovereign-release-<n>-delivery, which runs the candidate's own step 03 as its
# prewarm) failed in the settled-roll pre-flight (Phase A0):
#   FATAL: env NOT settled - 3 PLATFORM-namespace release(s)/workload(s) are mid-roll:
#   HelmRelease/flux-system/bp-powerdns [TERMINAL: Flux stalled]
#     (Helm rollback to previous release ... bp-powerdns@1.2.30 succeeded)
#   bp-external-dns, bp-powerdns-admin (dependency flux-system/bp-powerdns is not ready)
# bp-powerdns 1.2.32 had been refused by an admission policy and rolled back;
# the candidate pinned 1.2.33, which repairs it. The delivery that carries the
# repair was refused because of the defect it repairs.
#
# Every case EXECUTES the gate block the chart renders (between its extraction
# anchors), with a stubbed kubectl and a stubbed srp_candidate_pins (the
# candidate's "<chart>\t<version>" pins, read in production from the local Gitea
# at UPSTREAM_BRANCH=release/<candidate>):
#   1. stalled release whose pin the candidate moves, two dependents: PASS, the
#      three names recorded in the status ConfigMap;
#   2. the candidate pins the version it stalled on: FATAL as before, and the
#      gate says why the candidate does not repair it;
#   3. a genuinely mid-roll release beside the repaired one: FATAL naming only
#      the mid-roll one; and a dependent that also waits on a mid-roll release
#      stays gated;
#   4. the first cutover (no delivery): FATAL as before, the candidate's pins
#      are not read, and only the delivery environment changes the verdict;
#   5. the pins name charts, not releases: a stalled release the kit does not
#      apply (an Organization's bp-keycloak in a namespace without the
#      Organization label; a flux-system release without the bootstrap-kit
#      label) stays FATAL, as does a chart the candidate pins at two versions;
#      a dependent of a dependent is repaired; an override is recorded beside;
#   6. the production srp_candidate_pins (no stub): the PAT read, the Gitea
#      listing at release/<candidate>, the real extractor with the local
#      prefix; and a listing that fails keeps the delivery FATAL.
# On the tree before the change, cases 1, 2, 3 and 4 fail; case 5 fails before
# the repair is restricted to the releases the kit applies.
#
# Usage: bash tests/delivery-repairs-stalled-release.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

for t in helm jq awk sed; do
  command -v "$t" >/dev/null 2>&1 || { echo "FAIL: $t is required (refusing to skip: a skipped gate is a fail-open gate)"; exit 1; }
done

helm template ssc "${CHART_DIR}" --set sovereign.fqdn=sov.example.test >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}
sed -n '/^  name: cutover-step-03-harbor-prewarm$/,/^---$/p' "${TMP}/render.yaml" >"${TMP}/step03.yaml"
awk '/^  run\.sh: \|$/{f=1;next} f && /^  [^ ]/{f=0} f{print}' "${TMP}/step03.yaml" | sed -e 's/^    //' >"${TMP}/runsh.txt"
sed -n '/settled-roll-preflight BEGIN/,/settled-roll-preflight END/p' "${TMP}/runsh.txt" >"${TMP}/block.sh"
tail -n 1 "${TMP}/block.sh" >"${TMP}/block.last"
if ! grep -q 'settled_roll_preflight()' "${TMP}/block.sh" || ! grep -q 'settled-roll-preflight END' "${TMP}/block.last"; then
  echo "FAIL: could not extract the settled-roll gate block from the rendered step 03 (anchors moved?)"; exit 1
fi

mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/bin/sh
case "$*" in
  *"get namespaces"*)
    case "$*" in *"-l openova.io/organization"*) ;; *) echo "stub: namespace listing without the organization selector: $*" >&2; exit 9 ;; esac
    printf '{"items":[]}' ;;
  *"get helmreleases"*) cat "${HR_FIXTURE:?}" ;;
  *"get secret catalyst-gitea-token -o jsonpath={.data.token}"*) printf '%s' "${KIT_PAT:?}" | base64 ;;
  *"get deployments,statefulsets"*) printf '{"items":[]}' ;;
  *"patch configmap"*) printf '%s\n' "$*" >>"${PATCH_LOG:?}" ;;
  *) echo "stub: unhandled kubectl $*" >&2; exit 9 ;;
esac
STUB
chmod +x "${TMP}/bin/kubectl"
{
  echo 'set -eu'
  # The candidate's pins: the fixture, and a mark that the gate read them.
  echo 'srp_candidate_pins() { echo read >>"${PINS_LOG:?}"; cat "${PINS_FIXTURE:?}"; }'
  cat "${TMP}/block.sh"
} >"${TMP}/gate.sh"

# The production srp_candidate_pins and the prewarm_kit_fetch it calls, as the
# chart renders them, over the extractor the chart renders (its ConfigMap); only
# the two mount paths point into the scratch folder.
sed -n '/^  name: cutover-bootstrap-kit-pins$/,/^---$/p' "${TMP}/render.yaml" \
  | awk '/^  extract-pins\.sh: \|$/{f=1;next} f && /^  [^ ]|^---$/{f=0} f{print}' | sed -e 's/^    //' >"${TMP}/extract-pins.sh"
grep -q '^bootstrapkit_pins() {$' "${TMP}/extract-pins.sh" || { echo "FAIL: could not extract extract-pins.sh from the rendered ConfigMap"; exit 1; }
awk '/^prewarm_kit_fetch\(\) \{$/{f=1} f{print} f && /^srp_candidate_pins\(\) \{$/{s=1} s && /^}$/{exit}' "${TMP}/runsh.txt" \
  | sed -e "s#/pin-extractor/extract-pins.sh#${TMP}/extract-pins.sh#g" -e "s#/tmp/bootstrap-kit-pins#${TMP}/kit-pins#g" >"${TMP}/fetch.sh"
grep -q '^srp_candidate_pins() {$' "${TMP}/fetch.sh" && grep -q 'bootstrapkit_pins ' "${TMP}/fetch.sh" && [ "$(tail -n 1 "${TMP}/fetch.sh")" = '}' ] \
  || { echo "FAIL: could not extract prewarm_kit_fetch and srp_candidate_pins from the rendered step 03"; exit 1; }
{ echo 'set -eu'; cat "${TMP}/fetch.sh" "${TMP}/block.sh"; } >"${TMP}/gate-real.sh"
# The local Gitea: the token, the listing and each raw file, all at the
# candidate's branch; anything else answers as curl -f does on a 404.
cat >"${TMP}/bin/curl" <<'STUB'
#!/bin/sh
out=""; auth=""; url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -H) auth="$2"; shift 2 ;;
    --retry|--retry-delay) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\n' "${url}" >>"${CURL_LOG:?}"
[ "${auth}" = "Authorization: token ${KIT_PAT:?}" ] || exit 22
case "${url}" in *"?ref=${KIT_REF:?}") ;; *) exit 22 ;; esac
base=http://gitea-http.gitea.svc:3000/api/v1/repos/openova/openova
case "${url}" in
  "${base}/contents/clusters/_template/bootstrap-kit?"*)
    [ "${KIT_LISTING:-ok}" = ok ] || exit 22
    printf '[{"type":"dir","name":"0-sub"}'
    for f in "${KIT_DIR:?}"/*; do printf ',{"type":"file","name":"%s"}' "${f##*/}"; done
    printf ']' ;;
  "${base}/raw/clusters/_template/bootstrap-kit/"*)
    f="${url#"${base}/raw/clusters/_template/bootstrap-kit/"}"; f="${f%%\?*}"
    [ -f "${KIT_DIR}/${f}" ] || exit 22
    cp "${KIT_DIR}/${f}" "${out:?}" ;;
  *) exit 22 ;;
esac
STUB
chmod +x "${TMP}/bin/curl"

CAND=20261125T101500Z-abc1234
# run <dir> <hr fixture> <pins fixture> <delivery: yes|no|branch>: the gate's
# output in <dir>/out, its exit code in <dir>/rc, the ConfigMap patches in
# <dir>/patch.log, the reads of the candidate's pins in <dir>/pins.log.
run() {
  : >"$1/patch.log"; : >"$1/pins.log"
  case "$4" in
    yes) set -- "$@" "${CAND}" "release/${CAND}" ;;
    branch) set -- "$@" "${CAND}" "main" ;;
    *) set -- "$@" "" "main" ;;
  esac
  : >"$1/curl.log"
  ( export HR_FIXTURE="$2" PINS_FIXTURE="$3" PATCH_LOG="$1/patch.log" PINS_LOG="$1/pins.log" CURL_LOG="$1/curl.log"
    export PATH="${TMP}/bin:${PATH}" STATUS_CONFIGMAP_NAME=self-sovereign-cutover-status STATUS_CONFIGMAP_NAMESPACE=catalyst
    export SETTLED_ROLL_PREFLIGHT=true UPSTREAM_BRANCH="$6"
    # Read only by the production srp_candidate_pins (case 6, GATE=gate-real.sh).
    export PAT_SOURCE_NAMESPACE=gitea PAT_SOURCE_SECRET_NAME=catalyst-gitea-token PAT_SOURCE_KEY=token
    export GITEA_INTERNAL_URL=http://gitea-http.gitea.svc:3000/ GITEA_ORG=openova GITEA_REPO=openova
    export UPSTREAM_PREFIX=oci://ghcr.io/openova-io HARBOR_PUBLIC_URL=https://registry.sov.example.test/
    export KIT_PAT=pat-of-the-local-gitea KIT_DIR="${TMP}/kit" KIT_REF="release/${CAND}" KIT_LISTING="${KIT_LISTING:-ok}"
    if [ -n "$5" ]; then export CANDIDATE="$5"; else unset CANDIDATE; fi
    sh "${GATE:-${TMP}/gate.sh}" ) >"$1/out" 2>&1
  echo "$?" >"$1/rc"
}
rc_of() { cat "$1/rc"; }
midroll() { grep '^  mid-roll: ' "$1/out" >"$1/midroll" || true; }

# The measured state: bp-powerdns stalled on 1.2.32 after the rollback to
# 1.2.30; its two dependents DependencyNotReady on it; their other
# dependencies settled. Every release the kit applies carries the labels Flux's
# Kustomization bootstrap-kit stamps on what it applies.
KIT='"labels":{"kustomize.toolkit.fluxcd.io/name":"bootstrap-kit","kustomize.toolkit.fluxcd.io/namespace":"flux-system"}'
settled() { printf '{"metadata":{"name":"%s","namespace":"flux-system",%s,"generation":2},"spec":{"chart":{"spec":{"chart":"%s","version":"%s"}}},"status":{"observedGeneration":2,"lastAttemptedRevision":"%s","conditions":[{"type":"Ready","status":"True","reason":"UpgradeSucceeded","message":"Helm upgrade succeeded"}]}}' "$1" "${KIT}" "$1" "$2" "$2"; }
cat >"${TMP}/measured.json" <<EOF
{"items":[
$(settled bp-cert-manager 1.4.0),
$(settled bp-reflector 1.0.2),
$(settled bp-cnpg 1.0.0),
{"metadata":{"name":"bp-powerdns","namespace":"flux-system",${KIT},"generation":7},
 "spec":{"chart":{"spec":{"chart":"bp-powerdns","version":"1.2.32","sourceRef":{"kind":"HelmRepository","name":"bp-powerdns"}}},"dependsOn":[{"name":"bp-cert-manager"}]},
 "status":{"observedGeneration":7,"lastAttemptedRevision":"1.2.32",
  "history":[{"chartName":"bp-powerdns","chartVersion":"1.2.30","status":"deployed","version":9},{"chartName":"bp-powerdns","chartVersion":"1.2.32","status":"failed","version":8}],
  "conditions":[
   {"type":"Ready","status":"False","reason":"RollbackSucceeded","message":"Helm rollback to previous release powerdns/powerdns.v8 with chart bp-powerdns@1.2.30 succeeded"},
   {"type":"Released","status":"False","reason":"UpgradeFailed","message":"Helm upgrade failed for release powerdns/powerdns with chart bp-powerdns@1.2.32: admission webhook denied the request"},
   {"type":"Stalled","status":"True","reason":"RetriesExceeded","message":"Failed to upgrade after 1 attempt(s)"}]}},
{"metadata":{"name":"bp-external-dns","namespace":"flux-system",${KIT},"generation":4},
 "spec":{"chart":{"spec":{"chart":"bp-external-dns","version":"1.1.5"}},"dependsOn":[{"name":"bp-cert-manager"},{"name":"bp-powerdns"},{"name":"bp-reflector"}]},
 "status":{"observedGeneration":3,"conditions":[{"type":"Ready","status":"False","reason":"DependencyNotReady","message":"dependency 'flux-system/bp-powerdns' is not ready"}]}},
{"metadata":{"name":"bp-powerdns-admin","namespace":"flux-system",${KIT},"generation":3},
 "spec":{"chart":{"spec":{"chart":"bp-powerdns-admin","version":"0.1.9"}},"dependsOn":[{"name":"bp-powerdns","namespace":"flux-system"},{"name":"bp-cnpg","namespace":"flux-system"}]},
 "status":{"observedGeneration":3,"conditions":[{"type":"Ready","status":"False","reason":"DependencyNotReady","message":"dependency 'flux-system/bp-powerdns' is not ready"}]}}
]}
EOF
jq -e '.items | length == 6' "${TMP}/measured.json" >/dev/null || { echo "FAIL: the measured fixture is not valid JSON"; exit 1; }
# The candidate's kit pins: the repair 1.2.33 (moved), or the version it stalled on (same).
printf 'bp-cert-manager\t1.4.0\nbp-cnpg\t1.0.0\nbp-external-dns\t1.1.5\nbp-powerdns\t1.2.33\nbp-powerdns-admin\t0.1.9\nbp-reflector\t1.0.2\n' >"${TMP}/pins-moved.tsv"
sed 's/^bp-powerdns\t1\.2\.33$/bp-powerdns\t1.2.32/' "${TMP}/pins-moved.tsv" >"${TMP}/pins-same.tsv"
grep -q "$(printf 'bp-powerdns\t1.2.32')" "${TMP}/pins-same.tsv" || { echo "FAIL: could not build the same-version pins fixture"; exit 1; }

echo "[delivery-repairs] 1. the candidate moves the stalled release's pin: the delivery passes and records the three"
d="${TMP}/c1"; mkdir -p "$d"
run "$d" "${TMP}/measured.json" "${TMP}/pins-moved.tsv" yes
[ "$(rc_of "$d")" = 0 ] && pass "the pre-flight passes" || fail "the pre-flight refused the delivery (rc $(rc_of "$d")): $(grep -m1 FATAL "$d/out")"
[ -s "$d/pins.log" ] && pass "it read the candidate's pins" || fail "it did not read the candidate's pins"
grep -qF 'R HelmRelease/flux-system/bp-powerdns: bp-powerdns 1.2.32 (last attempted; deployed 1.2.30) -> 1.2.33' "$d/out" \
  && pass "it names bp-powerdns with both versions" || fail "bp-powerdns is not named with 1.2.32 and 1.2.33"
for n in bp-external-dns bp-powerdns-admin; do
  grep -qF "D HelmRelease/flux-system/${n}: DependencyNotReady only on flux-system/bp-powerdns" "$d/out" \
    && pass "it names the dependent ${n}" || fail "the dependent ${n} is not named"
done
grep -F 'settledRollOverrides' "$d/patch.log" >"$d/record" || true
for n in bp-powerdns bp-external-dns bp-powerdns-admin; do
  grep -qF "flux-system/${n}=" "$d/record" \
    && pass "the status ConfigMap records flux-system/${n}" || fail "the status ConfigMap does not record flux-system/${n}"
done
grep -qF '1.2.32 -> 1.2.33' "$d/record" \
  && pass "the record carries both versions" || fail "the record lacks the versions"

echo "[delivery-repairs] 2. the candidate pins the version it stalled on: FATAL as before"
d="${TMP}/c2"; mkdir -p "$d"
run "$d" "${TMP}/measured.json" "${TMP}/pins-same.tsv" yes
midroll "$d"
[ "$(rc_of "$d")" != 0 ] && pass "the pre-flight fails" || fail "the pre-flight passed a stalled release the candidate does not change"
for n in bp-powerdns bp-external-dns bp-powerdns-admin; do
  grep -qF "HelmRelease/flux-system/${n}" "$d/midroll" && pass "${n} is still named mid-roll" || fail "${n} is not named mid-roll"
done
[ -s "$d/pins.log" ] && pass "it read the candidate's pins" || fail "it did not read the candidate's pins"
grep -qF 'N HelmRelease/flux-system/bp-powerdns: the candidate pins bp-powerdns 1.2.32, not a new version for it' "$d/out" \
  && pass "it says the candidate does not change bp-powerdns" || fail "it does not say why bp-powerdns is not repaired"
grep -q 'flux-system/bp-powerdns=' "$d/patch.log" && fail "a refused delivery recorded an exclusion" || pass "nothing is recorded"

echo "[delivery-repairs] 3. a genuinely mid-roll release beside the repaired one: FATAL naming only that one"
d="${TMP}/c3"; mkdir -p "$d"
jq '.items += [{"metadata":{"name":"bp-cilium","namespace":"flux-system","generation":5},"spec":{"chart":{"spec":{"chart":"bp-cilium","version":"1.19.4"}}},"status":{"observedGeneration":4,"lastAttemptedRevision":"1.19.3","conditions":[{"type":"Ready","status":"False","reason":"Progressing","message":"Running upgrade"}]}}]' \
  "${TMP}/measured.json" >"${TMP}/midroll.json"
run "$d" "${TMP}/midroll.json" "${TMP}/pins-moved.tsv" yes
midroll "$d"
[ "$(rc_of "$d")" != 0 ] && pass "the pre-flight fails" || fail "the pre-flight passed a genuinely mid-roll release"
grep -qF 'HelmRelease/flux-system/bp-cilium' "$d/midroll" && pass "it names bp-cilium" || fail "bp-cilium is not named mid-roll"
[ "$(grep -c . "$d/midroll")" = 1 ] && pass "it names only bp-cilium" || fail "it names more than the mid-roll release: $(tr '\n' ' ' <"$d/midroll")"
grep -q '=' "$d/patch.log" && fail "a refused delivery recorded an exclusion" || pass "nothing is recorded"
d="${TMP}/c3b"; mkdir -p "$d"
jq '(.items[] | select(.metadata.name == "bp-cnpg")) |= (.metadata.generation = 3 | .status.conditions[0] = {"type":"Ready","status":"False","reason":"Progressing","message":"Running upgrade"})' \
  "${TMP}/measured.json" >"${TMP}/cnpg-midroll.json"
run "$d" "${TMP}/cnpg-midroll.json" "${TMP}/pins-moved.tsv" yes
midroll "$d"
[ "$(rc_of "$d")" != 0 ] && pass "a dependent that also waits on a mid-roll release fails the pre-flight" || fail "the pre-flight passed bp-powerdns-admin, which also waits on a mid-roll bp-cnpg"
grep -qF 'HelmRelease/flux-system/bp-powerdns-admin' "$d/midroll" && pass "bp-powerdns-admin stays gated" || fail "bp-powerdns-admin is not named"
grep -qF 'HelmRelease/flux-system/bp-external-dns' "$d/midroll" && fail "bp-external-dns, which waits only on bp-powerdns, is gated" || pass "bp-external-dns, which waits only on bp-powerdns, is not gated"

echo "[delivery-repairs] 4. the first cutover is not a delivery: FATAL as before, the candidate's pins are not read"
d="${TMP}/c4"; mkdir -p "$d"
run "$d" "${TMP}/measured.json" "${TMP}/pins-moved.tsv" no
midroll "$d"
[ "$(rc_of "$d")" != 0 ] && pass "the pre-flight fails" || fail "the first cutover passed a stalled release"
[ "$(grep -c . "$d/midroll")" = 3 ] && pass "it names the three as before" || fail "it names: $(tr '\n' ' ' <"$d/midroll")"
grep -qF '[TERMINAL: Flux stalled' "$d/midroll" && pass "bp-powerdns is still tagged terminal" || fail "the terminal tag is gone"
[ -s "$d/pins.log" ] && fail "the first cutover read a candidate's pins" || pass "no candidate pins are read"
grep -q 'delivery of' "$d/out" && fail "the first cutover speaks of a delivery" || pass "no delivery line"
d="${TMP}/c4b"; mkdir -p "$d"
run "$d" "${TMP}/measured.json" "${TMP}/pins-moved.tsv" branch
[ "$(rc_of "$d")" != 0 ] && [ ! -s "$d/pins.log" ] && pass "CANDIDATE without UPSTREAM_BRANCH=release/<candidate> is no delivery" || fail "a run off release/<candidate> was taken for a delivery"
# The control: the same fixture and pins with the delivery environment pass.
# Only the delivery changes the verdict (case 1 runs exactly this).
[ "$(rc_of "${TMP}/c1")" = 0 ] && [ "$(rc_of "$d")" != 0 ] && pass "only the delivery environment changes the verdict" || fail "the verdict does not follow the delivery environment (delivery rc $(rc_of "${TMP}/c1"), first cutover rc $(rc_of "$d"))"

echo "[delivery-repairs] 5. the pins name charts, not releases: a stalled release the kit does not apply stays FATAL"
# An Organization's releases as organization_gitops.go writes them (<org-ns>/bp-keycloak,
# version "*", bp-newapi depending on it) in a namespace WITHOUT the Organization
# label, which the gate treats as platform; the candidate pins another bp-keycloak.
cat >"${TMP}/org-shape.json" <<'EOF'
{"items":[
{"metadata":{"name":"bp-keycloak","namespace":"delta-corp","generation":4},
 "spec":{"chart":{"spec":{"chart":"bp-keycloak","version":"*"}}},
 "status":{"observedGeneration":4,"lastAttemptedRevision":"1.3.9",
  "history":[{"chartName":"bp-keycloak","chartVersion":"1.3.8","status":"deployed","version":3}],
  "conditions":[{"type":"Ready","status":"False","reason":"UpgradeFailed","message":"Helm upgrade failed for release delta-corp/keycloak"},
   {"type":"Stalled","status":"True","reason":"RetriesExceeded","message":"Failed to upgrade after 1 attempt(s)"}]}},
{"metadata":{"name":"bp-newapi","namespace":"delta-corp","generation":2},
 "spec":{"chart":{"spec":{"chart":"bp-newapi","version":"*"}},"dependsOn":[{"name":"bp-keycloak"}]},
 "status":{"observedGeneration":2,"conditions":[{"type":"Ready","status":"False","reason":"DependencyNotReady","message":"dependency 'delta-corp/bp-keycloak' is not ready"}]}}
]}
EOF
printf 'bp-keycloak\t1.4.0\nbp-newapi\t0.2.0\n' >"${TMP}/pins-keycloak.tsv"
d="${TMP}/c5"; mkdir -p "$d"
run "$d" "${TMP}/org-shape.json" "${TMP}/pins-keycloak.tsv" yes
midroll "$d"
[ "$(rc_of "$d")" != 0 ] && pass "the pre-flight fails" || fail "the pre-flight passed a stalled release the promote never touches"
for n in bp-keycloak bp-newapi; do
  grep -qF "HelmRelease/delta-corp/${n}" "$d/midroll" && pass "delta-corp/${n} is still named mid-roll" || fail "delta-corp/${n} is not named mid-roll"
done
grep -qF 'N HelmRelease/delta-corp/bp-keycloak: the kit does not apply it' "$d/out" \
  && pass "it says the kit does not apply delta-corp/bp-keycloak" || fail "it does not say why delta-corp/bp-keycloak is not repaired"
grep -qE '^  [RD] ' "$d/out" && fail "a release the kit does not apply was counted as repaired: $(grep -E '^  [RD] ' "$d/out" | tr '\n' ' ')" || pass "nothing is counted as repaired"
grep -q '=' "$d/patch.log" && fail "a refused delivery recorded an exclusion" || pass "nothing is recorded"
d="${TMP}/c5b"; mkdir -p "$d"
jq '(.items[] | select(.metadata.name == "bp-powerdns")) |= del(.metadata.labels)' "${TMP}/measured.json" >"${TMP}/unlabelled.json"
run "$d" "${TMP}/unlabelled.json" "${TMP}/pins-moved.tsv" yes
midroll "$d"
[ "$(rc_of "$d")" != 0 ] && [ "$(grep -c . "$d/midroll")" = 3 ] && pass "a flux-system bp-powerdns that the Kustomization bootstrap-kit did not apply stays FATAL with its dependents" \
  || fail "a flux-system release without the bootstrap-kit label was repaired (rc $(rc_of "$d")): $(tr '\n' ' ' <"$d/midroll")"
grep -qF 'N HelmRelease/flux-system/bp-powerdns: the kit does not apply it' "$d/out" && pass "it says why" || fail "it does not say why bp-powerdns is not repaired"
d="${TMP}/c5c"; mkdir -p "$d"
{ cat "${TMP}/pins-moved.tsv"; printf 'bp-powerdns\t1.2.34\n'; } >"${TMP}/pins-two.tsv"
run "$d" "${TMP}/measured.json" "${TMP}/pins-two.tsv" yes
[ "$(rc_of "$d")" != 0 ] && pass "a chart the candidate pins at two versions stays FATAL" || fail "the pre-flight passed a chart pinned at two versions"
grep -qF 'N HelmRelease/flux-system/bp-powerdns: the candidate kit pins bp-powerdns at several versions (1.2.33, 1.2.34)' "$d/out" \
  && pass "it names both versions" || fail "it does not say the chart is pinned at several versions"
d="${TMP}/c5d"; mkdir -p "$d"
jq --argjson kit "{${KIT}}" '.items += [
  {"metadata":({"name":"bp-pdns-dashboard","namespace":"flux-system","generation":2} + $kit),
   "spec":{"chart":{"spec":{"chart":"bp-pdns-dashboard","version":"0.1.0"}},"dependsOn":[{"name":"bp-powerdns-admin"}]},
   "status":{"observedGeneration":2,"conditions":[{"type":"Ready","status":"False","reason":"DependencyNotReady","message":"dependency flux-system/bp-powerdns-admin is not ready"}]}},
  {"metadata":({"name":"bp-velero","namespace":"flux-system","generation":3,"annotations":{"catalyst.openova.io/cutover-settled-roll-override":"quota-wedged, ticket 5393"}} + $kit),
   "spec":{"chart":{"spec":{"chart":"bp-velero","version":"1.0.4"}}},
   "status":{"observedGeneration":3,"lastAttemptedRevision":"1.0.4","conditions":[{"type":"Ready","status":"False","reason":"UpgradeFailed","message":"quota exceeded"},{"type":"Stalled","status":"True","reason":"RetriesExceeded","message":"Failed to upgrade after 1 attempt(s)"}]}}]' \
  "${TMP}/measured.json" >"${TMP}/chain.json"
run "$d" "${TMP}/chain.json" "${TMP}/pins-moved.tsv" yes
[ "$(rc_of "$d")" = 0 ] && pass "a dependent of a dependent and an override beside the repair pass" || fail "the pre-flight refused (rc $(rc_of "$d")): $(grep -m1 FATAL "$d/out")"
grep -qF 'D HelmRelease/flux-system/bp-pdns-dashboard: DependencyNotReady only on flux-system/bp-powerdns-admin' "$d/out" \
  && pass "the dependent of a dependent is named" || fail "bp-pdns-dashboard is not named as a dependent"
grep -F 'settledRollOverrides' "$d/patch.log" >"$d/record" || true
for r in 'flux-system/bp-velero=quota-wedged, ticket 5393' 'flux-system/bp-powerdns=the delivery of' 'flux-system/bp-pdns-dashboard=DependencyNotReady only on'; do
  grep -qF "$r" "$d/record" && pass "the record carries ${r%%=*}" || fail "the record lacks ${r%%=*}"
done

echo "[delivery-repairs] 6. the production srp_candidate_pins: the candidate's pins read from the local Gitea at release/<candidate>"
# The candidate's kit at release/<candidate>: bp-powerdns re-pinned to 1.2.33 on
# the already-pivoted local repository, bp-cert-manager on the upstream one, and
# a file that is no slot.
mkdir -p "${TMP}/kit"
cat >"${TMP}/kit/30-powerdns.yaml" <<'EOF'
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: bp-powerdns
  namespace: flux-system
spec:
  type: oci
  url: oci://registry.sov.example.test/openova-io
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-powerdns
  namespace: flux-system
spec:
  chart:
    spec:
      chart: bp-powerdns
      # 1.2.33 repairs the refused 1.2.32
      version: 1.2.33
      sourceRef:
        kind: HelmRepository
        name: bp-powerdns
        namespace: flux-system
EOF
sed -e 's/powerdns/cert-manager/g' -e 's,registry\.sov\.example\.test,ghcr.io,' -e 's/1\.2\.33/1.4.0/g' -e 's/^      # .*$/      # unchanged/' \
  "${TMP}/kit/30-powerdns.yaml" >"${TMP}/kit/02-cert-manager.yaml"
printf 'resources: []\n' >"${TMP}/kit/kustomization.yaml"
d="${TMP}/c6"; mkdir -p "$d"
GATE="${TMP}/gate-real.sh" run "$d" "${TMP}/measured.json" /dev/null yes
[ "$(rc_of "$d")" = 0 ] && pass "the pre-flight passes on the pins it read" || fail "the pre-flight refused the delivery (rc $(rc_of "$d")): $(grep -m1 FATAL "$d/out")"
grep -qF 'R HelmRelease/flux-system/bp-powerdns: bp-powerdns 1.2.32 (last attempted; deployed 1.2.30) -> 1.2.33' "$d/out" \
  && pass "it read bp-powerdns 1.2.33 off the local repository" || fail "bp-powerdns 1.2.33 was not read: $(grep -m2 'FATAL\|could not' "$d/out" | tr '\n' ' ')"
grep -qF "contents/clusters/_template/bootstrap-kit?ref=release/${CAND}" "$d/curl.log" && pass "it listed the kit at release/${CAND}" || fail "it did not list the kit at release/${CAND}"
[ "$(grep -c '/raw/' "$d/curl.log")" = 2 ] && pass "it fetched the two slot files and no other" || fail "it fetched: $(grep '/raw/' "$d/curl.log" | tr '\n' ' ')"
d="${TMP}/c6b"; mkdir -p "$d"
KIT_LISTING=fail GATE="${TMP}/gate-real.sh" run "$d" "${TMP}/measured.json" /dev/null yes
midroll "$d"
[ "$(rc_of "$d")" != 0 ] && [ "$(grep -c . "$d/midroll")" = 3 ] && pass "a listing that fails keeps the delivery FATAL on all three" || fail "the delivery passed without its pins (rc $(rc_of "$d"))"
grep -qF "could not read its candidate's kit pins at release/${CAND}" "$d/out" && pass "it says it could not read the pins" || fail "it does not say the pins were unreadable"
grep -q '=' "$d/patch.log" && fail "a refused delivery recorded an exclusion" || pass "nothing is recorded"

echo
if [ "${FAILURES}" -gt 0 ]; then echo "delivery-repairs-stalled-release: ${FAILURES} failure(s)"; exit 1; fi
echo "delivery-repairs-stalled-release: all cases passed"
