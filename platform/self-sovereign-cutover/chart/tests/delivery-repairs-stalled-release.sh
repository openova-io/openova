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
#      are not read, and only the delivery environment changes the verdict.
# On the tree before the change, cases 1, 2, 3 and 4 fail.
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
  ( export HR_FIXTURE="$2" PINS_FIXTURE="$3" PATCH_LOG="$1/patch.log" PINS_LOG="$1/pins.log"
    export PATH="${TMP}/bin:${PATH}" STATUS_CONFIGMAP_NAME=self-sovereign-cutover-status STATUS_CONFIGMAP_NAMESPACE=catalyst
    export SETTLED_ROLL_PREFLIGHT=true UPSTREAM_BRANCH="$6"
    if [ -n "$5" ]; then export CANDIDATE="$5"; else unset CANDIDATE; fi
    sh "${TMP}/gate.sh" ) >"$1/out" 2>&1
  echo "$?" >"$1/rc"
}
rc_of() { cat "$1/rc"; }
midroll() { grep '^  mid-roll: ' "$1/out" >"$1/midroll" || true; }

# The measured state: bp-powerdns stalled on 1.2.32 after the rollback to
# 1.2.30; its two dependents DependencyNotReady on it; their other
# dependencies settled.
settled() { printf '{"metadata":{"name":"%s","namespace":"flux-system","generation":2},"spec":{"chart":{"spec":{"chart":"%s","version":"%s"}}},"status":{"observedGeneration":2,"lastAttemptedRevision":"%s","conditions":[{"type":"Ready","status":"True","reason":"UpgradeSucceeded","message":"Helm upgrade succeeded"}]}}' "$1" "$1" "$2" "$2"; }
cat >"${TMP}/measured.json" <<EOF
{"items":[
$(settled bp-cert-manager 1.4.0),
$(settled bp-reflector 1.0.2),
$(settled bp-cnpg 1.0.0),
{"metadata":{"name":"bp-powerdns","namespace":"flux-system","generation":7},
 "spec":{"chart":{"spec":{"chart":"bp-powerdns","version":"1.2.32","sourceRef":{"kind":"HelmRepository","name":"bp-powerdns"}}},"dependsOn":[{"name":"bp-cert-manager"}]},
 "status":{"observedGeneration":7,"lastAttemptedRevision":"1.2.32",
  "history":[{"chartName":"bp-powerdns","chartVersion":"1.2.30","status":"deployed","version":9},{"chartName":"bp-powerdns","chartVersion":"1.2.32","status":"failed","version":8}],
  "conditions":[
   {"type":"Ready","status":"False","reason":"RollbackSucceeded","message":"Helm rollback to previous release powerdns/powerdns.v8 with chart bp-powerdns@1.2.30 succeeded"},
   {"type":"Released","status":"False","reason":"UpgradeFailed","message":"Helm upgrade failed for release powerdns/powerdns with chart bp-powerdns@1.2.32: admission webhook denied the request"},
   {"type":"Stalled","status":"True","reason":"RetriesExceeded","message":"Failed to upgrade after 1 attempt(s)"}]}},
{"metadata":{"name":"bp-external-dns","namespace":"flux-system","generation":4},
 "spec":{"chart":{"spec":{"chart":"bp-external-dns","version":"1.1.5"}},"dependsOn":[{"name":"bp-cert-manager"},{"name":"bp-powerdns"},{"name":"bp-reflector"}]},
 "status":{"observedGeneration":3,"conditions":[{"type":"Ready","status":"False","reason":"DependencyNotReady","message":"dependency 'flux-system/bp-powerdns' is not ready"}]}},
{"metadata":{"name":"bp-powerdns-admin","namespace":"flux-system","generation":3},
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

echo
if [ "${FAILURES}" -gt 0 ]; then echo "delivery-repairs-stalled-release: ${FAILURES} failure(s)"; exit 1; fi
echo "delivery-repairs-stalled-release: all cases passed"
